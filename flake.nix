{
  description = "Ovid: a small compiled language whose toolchain is built for agents";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;
      # Not x86_64-darwin: nixpkgs dropped it in 26.11 and refuses to evaluate it.
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
      forAll = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # Only what each derivation reads, so editing the README or this file
      # does not rebuild the compiler. std/ is embedded in the binary.
      toolSrc = lib.fileset.toSource {
        root = ./.;
        fileset = lib.fileset.unions [ ./go.mod ./cmd ./internal ./std ];
      };
      testSrc = lib.fileset.toSource {
        root = ./.;
        fileset = lib.fileset.unions [ ./go.mod ./cmd ./internal ./std ./prog ./tests ];
      };

      # ovid version reads the commit from the Go VCS stamp, which a build
      # outside a checkout lacks; main.commit carries it instead.
      commit = self.rev or self.dirtyRev or "";
      version = "0-" + (if commit == "" then "unknown" else builtins.substring 0 12 commit);
    in
    {
      # buildOvidProgram pkgs { pname; src; }: an Ovid module directory to a
      # static binary in $out/bin, with its build receipt (size, system calls)
      # in $out/share/ovid.
      lib.buildOvidProgram = pkgs: { pname, src, version ? "0", ovid ? self.packages.${pkgs.stdenv.hostPlatform.system}.ovid }:
        pkgs.runCommand "${pname}-${version}" { nativeBuildInputs = [ ovid ]; meta.mainProgram = pname; } ''
          mkdir -p $out/bin $out/share/ovid
          ovid build -C ${src} -o $out/bin/${pname} > $out/share/ovid/${pname}.receipt.json
        '';

      # syscallFilter pkgs { program; unit; }: a systemd drop-in for unit
      # (put it in systemd.packages) that allows the system calls program's
      # build receipt lists. systemd adds the few it always allows (its
      # @default set: brk, futex, clocks, getrandom, ...); every other call
      # kills the process with SIGSYS. The receipt has x86-64 numbers;
      # systemd wants names, which come from the kernel headers, so this is
      # done at build time and the module needs no import from derivation.
      lib.syscallFilter = pkgs: { program, unit }:
        pkgs.runCommand "${unit}-syscalls" { nativeBuildInputs = [ pkgs.jq ]; } ''
          receipts=(${program}/share/ovid/*.receipt.json)
          if [ ''${#receipts[@]} != 1 ] || [ ! -e "''${receipts[0]}" ]; then
            echo "want one build receipt in ${program}/share/ovid" >&2
            exit 1
          fi
          # syscalls_unknown means the list is incomplete, so a filter made
          # from it could kill the program on a call it does make.
          if ! jq -e '(.syscalls_unknown // 0) == 0' "''${receipts[0]}" > /dev/null; then
            echo "${program}: the receipt's system calls are incomplete (syscalls_unknown)" >&2
            exit 1
          fi
          table=${pkgs.linuxHeaders}/include/asm/unistd_64.h
          names=
          for n in $(jq -r '.syscalls[]' "''${receipts[0]}"); do
            name=$(awk -v n="$n" '$1 == "#define" && $2 ~ /^__NR_/ && $3 == n { print substr($2, 6) }' $table)
            if [ -z "$name" ]; then
              echo "system call $n is not in $table" >&2
              exit 1
            fi
            names="$names $name"
          done
          mkdir -p $out/lib/systemd/system/${unit}.d
          printf '[Service]\nSystemCallArchitectures=native\nSystemCallFilter=%s\n' "''${names# }" \
            > $out/lib/systemd/system/${unit}.d/ovid-syscalls.conf
        '';

      # services.ovid.programs.<name> = { package; args; listen; }: run a
      # program from buildOvidProgram as ovid-<name>.service, sandboxed,
      # under the filter syscallFilter makes from its build receipt; with
      # listen, as ovid-<name>@.service once per connection to
      # ovid-<name>.socket.
      nixosModules.default = { config, lib, pkgs, utils, ... }:
        let
          cfg = config.services.ovid.programs;
          # One service per connection when it listens: a template.
          service = name: p: if p.listen == null then "ovid-${name}" else "ovid-${name}@";
        in
        {
          options.services.ovid.programs = lib.mkOption {
            default = { };
            description = "Ovid programs to run as systemd services, each allowed the system calls its build receipt lists (and systemd's @default set).";
            type = lib.types.attrsOf (lib.types.submodule {
              options = {
                package = lib.mkOption {
                  type = lib.types.package;
                  description = "The program, built by lib.buildOvidProgram (its receipt is in share/ovid).";
                };
                args = lib.mkOption {
                  type = lib.types.listOf lib.types.str;
                  default = [ ];
                  description = "Arguments to the program.";
                };
                listen = lib.mkOption {
                  type = lib.types.nullOr (lib.types.either lib.types.port lib.types.str);
                  default = null;
                  example = 8080;
                  description = ''
                    Serve connections here instead of running once at boot: a
                    port, or any systemd ListenStream= value ("[::1]:8080",
                    "/run/greet.sock"). systemd accepts each connection and
                    starts ovid-<name>@.service with it as standard input and
                    output, which is what the host ovid build writes for an
                    HTTP handler reads and writes.
                  '';
                };
              };
            });
          };

          config = lib.mkIf (cfg != { }) {
            assertions = [{
              assertion = pkgs.stdenv.hostPlatform.system == "x86_64-linux";
              message = "services.ovid.programs: Ovid programs are Linux x86-64 binaries";
            }];
            systemd.packages = lib.mapAttrsToList
              (name: p: self.lib.syscallFilter pkgs { program = p.package; unit = "${service name p}.service"; })
              cfg;
            systemd.services = lib.mapAttrs'
              (name: p: lib.nameValuePair (service name p) {
                wantedBy = lib.optionals (p.listen == null) [ "multi-user.target" ];
                # A timed-out instance would otherwise stay loaded, failed,
                # one per such connection.
                unitConfig = lib.optionalAttrs (p.listen != null) { CollectMode = "inactive-or-failed"; };
                serviceConfig = {
                  ExecStart = utils.escapeSystemdExecArgs ([ (lib.getExe p.package) ] ++ p.args);
                  DynamicUser = true;
                  ProtectSystem = "strict";
                  ProtectHome = true;
                  PrivateTmp = true;
                  PrivateDevices = true;
                  NoNewPrivileges = true;
                  MemoryDenyWriteExecute = true;
                } // lib.optionalAttrs (p.listen != null) {
                  # The connection is standard input and output, so the
                  # stdio host serves it as it would a pipe.
                  StandardInput = "socket";
                  StandardOutput = "socket";
                  StandardError = "journal";
                  # The host waits on read with no timeout, so a peer that
                  # sends nothing would hold its instance, and one of the
                  # socket's connection slots, for good. Bound each
                  # connection's life.
                  RuntimeMaxSec = lib.mkDefault "60s";
                };
              })
              cfg;
            systemd.sockets = lib.mapAttrs'
              (name: p: lib.nameValuePair "ovid-${name}" {
                wantedBy = [ "sockets.target" ];
                listenStreams = [ (toString p.listen) ];
                socketConfig = {
                  Accept = true;
                  # Of the 64 connections systemd allows, one peer gets 8.
                  MaxConnectionsPerSource = lib.mkDefault 8;
                };
              })
              (lib.filterAttrs (_: p: p.listen != null) cfg);
          };
        };

      packages = forAll (pkgs:
        let
          ovid = pkgs.buildGoModule {
            pname = "ovid";
            inherit version;
            src = toolSrc;
            vendorHash = null; # go.mod has no dependencies
            subPackages = [ "cmd/ovid" ];
            env.CGO_ENABLED = 0;
            ldflags = [ "-s" "-w" "-X main.commit=${commit}" ];
            doCheck = false; # checks.go-test runs the whole suite
            meta.mainProgram = "ovid";
          };

          # The self-hosted compiler at its fixed point: Go builds s1, s1
          # builds itself, and the two must be byte-identical.
          ovid-selfhost = pkgs.runCommand "ovid-selfhost-${version}" { nativeBuildInputs = [ ovid ]; } ''
            cp -r ${testSrc}/prog prog
            chmod -R u+w prog
            ovid build -C prog -o s1
            ./s1 build prog -o s2 --std ${testSrc}/std
            cmp s1 s2
            mkdir -p $out/bin
            cp s1 $out/bin/ovid-selfhost
          '';

          hello = self.lib.buildOvidProgram pkgs { pname = "hello"; src = ./nix/example; };
        in
        {
          inherit ovid hello;
          default = ovid;
        } // lib.optionalAttrs (pkgs.stdenv.hostPlatform.system == "x86_64-linux") {
          # Ovid's output runs only on Linux x86-64: ovid-selfhost runs s1,
          # and an image built elsewhere would be tagged for an architecture
          # its binary is not.
          inherit ovid-selfhost;
          # The program and nothing else: no libc, no shell, no loader.
          hello-image = pkgs.dockerTools.buildImage {
            name = "ovid-hello";
            tag = "latest";
            copyToRoot = [ hello ];
            config.Entrypoint = [ "/bin/hello" ];
          };
        });

      checks = forAll (pkgs:
        let
          inherit (self.packages.${pkgs.stdenv.hostPlatform.system}) ovid hello;
          catfile = self.lib.buildOvidProgram pkgs { pname = "catfile"; src = ./nix/catfile; };
          greet = self.lib.buildOvidProgram pkgs { pname = "greet"; src = ./nix/greet; };
        in
        {
          go-test = pkgs.buildGoModule {
            pname = "ovid-go-test";
            version = "0"; # not the commit: a new commit alone should not rerun the suite
            src = testSrc;
            vendorHash = null;
            env.CGO_ENABLED = 0;
            buildPhase = "runHook preBuild; go vet ./...; runHook postBuild";
            checkPhase = "runHook preCheck; go test -count=1 ./...; runHook postCheck";
            installPhase = "touch $out";
          };
        } // lib.optionalAttrs (pkgs.stdenv.hostPlatform.system == "x86_64-linux") {
          # The rest run Ovid output, which runs only on Linux x86-64.
          prog = pkgs.runCommand "ovid-prog-check" { nativeBuildInputs = [ ovid ]; } ''
            cp -r ${testSrc}/prog prog
            chmod -R u+w prog
            ovid check -C prog
            ovid test -C prog
            touch $out
          '';
          selfhost = self.packages.x86_64-linux.ovid-selfhost;
          # A NixOS machine, where /lib64/ld-linux-x86-64.so.2 is stub-ld and
          # refuses every program: Ovid's static output runs as it is.
          nixos = pkgs.testers.runNixOSTest {
            name = "ovid-on-nixos";
            nodes.machine = {
              imports = [ self.nixosModules.default ];
              environment.systemPackages = [ ovid hello catfile pkgs.curl ];
              services.ovid.programs = {
                hello.package = hello;
                cat = { package = catfile; args = [ "/etc/os-release" ]; };
                greet = { package = greet; listen = 8080; };
              };
              systemd.services."ovid-greet@".serviceConfig.RuntimeMaxSec = "3s";
            };
            testScript = ''
              machine.wait_for_unit("multi-user.target")
              machine.succeed("readlink -f /lib64/ld-linux-x86-64.so.2 | grep stub-ld")
              machine.succeed("hello | grep 'hello from'")

              # Each service's drop-in lists its receipt's system calls, and
              # systemd loaded it: hello's filter has no openat, cat's does.
              machine.succeed("grep -x 'SystemCallFilter=write mmap exit' /etc/systemd/system/ovid-hello.service.d/ovid-syscalls.conf")
              machine.succeed("grep -x 'SystemCallFilter=read write close fstat mmap exit openat' /etc/systemd/system/ovid-cat.service.d/ovid-syscalls.conf")
              machine.succeed("systemctl show ovid-cat -p SystemCallFilter --value | grep -w openat")
              machine.fail("systemctl show ovid-hello -p SystemCallFilter --value | grep -w openat")
              machine.wait_until_succeeds("journalctl -u ovid-hello --no-pager | grep 'hello from'")
              machine.wait_until_succeeds("journalctl -u ovid-cat --no-pager | grep 'ID=nixos'")
              for unit in ["ovid-hello", "ovid-cat"]:
                  machine.wait_until_succeeds(f"systemctl show {unit} -p ExecMainCode --value | grep -x 1")
                  machine.succeed(f"systemctl show {unit} -p ExecMainStatus --value | grep -x 0")

              # The filter is enforced: catfile under hello's list is killed.
              out = machine.fail("systemd-run --wait --collect -p DynamicUser=yes -p 'SystemCallFilter=write mmap exit' ${lib.getExe catfile} /etc/os-release 2>&1")
              assert "status=31/SYS" in out, out

              # An HTTP handler behind socket activation: each connection is
              # one ovid-greet@ instance, under the handler's own filter. Two
              # URLs in one curl share a connection, so one host serves both.
              machine.wait_for_unit("ovid-greet.socket")
              machine.succeed("grep -x 'SystemCallFilter=read write mmap munmap madvise exit' '/etc/systemd/system/ovid-greet@.service.d/ovid-syscalls.conf'")
              out = machine.succeed("curl -fsS 'http://127.0.0.1:8080/hello?nixos'")
              assert out == "hello, nixos\n", out
              out = machine.succeed("curl -sS -w '%{http_code} %{num_connects}\\n' 'http://127.0.0.1:8080/hello?a' 'http://127.0.0.1:8080/nope'")
              assert out == "hello, a\n200 1\n404 0\n", out
              machine.succeed("test $(systemctl show ovid-greet.socket -p NAccepted --value) = 2")
              # A connection that sends nothing is closed when its instance
              # runs out of time (3 s here, not the 60 s default), and the
              # failed instance does not stay behind.
              machine.succeed("timeout 15 bash -c 'exec 3<>/dev/tcp/127.0.0.1/8080; cat <&3'")
              machine.wait_until_succeeds("test -z \"$(systemctl list-units --all --plain --no-legend 'ovid-greet@*')\"", timeout=30)

              machine.succeed("cp -r ${./nix/example} /tmp/ex && chmod -R u+w /tmp/ex")
              machine.succeed("cd /tmp/ex && ovid build -o /tmp/h && /tmp/h | grep 'hello from'")
              machine.succeed("cd /tmp/ex && ovid run --json | grep '\"confined\":\\[\"seccomp\",\"landlock\"\\]'")
              machine.succeed("cp -r ${testSrc}/prog /tmp/prog && chmod -R u+w /tmp/prog")
              machine.succeed("ovid test -C /tmp/prog | tail -1 | grep '\"ok\":true'")
            '';
          };
        });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls pkgs.jq self.packages.${pkgs.stdenv.hostPlatform.system}.ovid ];
        };
      });
    };
}
