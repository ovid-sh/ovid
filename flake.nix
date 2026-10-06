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

      # services.ovid.programs.<name> = { package; args; }: run a program
      # from buildOvidProgram as ovid-<name>.service, sandboxed, under the
      # filter syscallFilter makes from its build receipt.
      nixosModules.default = { config, lib, pkgs, utils, ... }:
        let
          cfg = config.services.ovid.programs;
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
              };
            });
          };

          config = lib.mkIf (cfg != { }) {
            assertions = [{
              assertion = pkgs.stdenv.hostPlatform.system == "x86_64-linux";
              message = "services.ovid.programs: Ovid programs are Linux x86-64 binaries";
            }];
            systemd.packages = lib.mapAttrsToList
              (name: p: self.lib.syscallFilter pkgs { program = p.package; unit = "ovid-${name}.service"; })
              cfg;
            systemd.services = lib.mapAttrs'
              (name: p: lib.nameValuePair "ovid-${name}" {
                wantedBy = [ "multi-user.target" ];
                serviceConfig = {
                  ExecStart = utils.escapeSystemdExecArgs ([ (lib.getExe p.package) ] ++ p.args);
                  DynamicUser = true;
                  ProtectSystem = "strict";
                  ProtectHome = true;
                  PrivateTmp = true;
                  PrivateDevices = true;
                  NoNewPrivileges = true;
                  MemoryDenyWriteExecute = true;
                };
              })
              cfg;
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
              environment.systemPackages = [ ovid hello catfile ];
              services.ovid.programs = {
                hello.package = hello;
                cat = { package = catfile; args = [ "/etc/os-release" ]; };
              };
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
