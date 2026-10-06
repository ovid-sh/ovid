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
        pkgs.runCommand "${pname}-${version}" { nativeBuildInputs = [ ovid ]; } ''
          mkdir -p $out/bin $out/share/ovid
          ovid build -C ${src} -o $out/bin/${pname} > $out/share/ovid/${pname}.receipt.json
        '';

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
          # It runs s1, and Ovid's output runs only on Linux x86-64.
          inherit ovid-selfhost;
        } // lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
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
          inherit (self.packages.${pkgs.stdenv.hostPlatform.system}) ovid;
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
          prog =pkgs.runCommand "ovid-prog-check" { nativeBuildInputs = [ ovid ]; } ''
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
              environment.systemPackages = [ ovid self.packages.x86_64-linux.hello ];
              systemd.services.hello = {
                wantedBy = [ "multi-user.target" ];
                serviceConfig = {
                  Type = "oneshot";
                  ExecStart = lib.getExe' self.packages.x86_64-linux.hello "hello";
                  DynamicUser = true;
                  ProtectSystem = "strict";
                  PrivateNetwork = true;
                };
              };
            };
            testScript = ''
              machine.wait_for_unit("multi-user.target")
              machine.succeed("readlink -f /lib64/ld-linux-x86-64.so.2 | grep stub-ld")
              machine.succeed("hello | grep 'hello from'")
              machine.succeed("journalctl -u hello --no-pager | grep 'hello from'")
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
