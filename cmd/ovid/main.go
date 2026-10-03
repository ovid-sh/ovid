package main

import (
	"fmt"
	"os"

	"ovid/internal/tool"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "check":
		if len(os.Args) != 3 {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.Check(os.Args[2], os.Stdout))
	case "query":
		if len(os.Args) < 3 {
			usage()
			os.Exit(1)
		}
		id, name, pkg, kind, err := queryFlags(os.Args[3:])
		if err != nil {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.Query(os.Args[2], id, name, pkg, kind, os.Stdout))
	case "patch":
		if len(os.Args) != 4 {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.Patch(os.Args[2], os.Args[3], os.Stdout))
	case "build":
		dir, out, err := buildFlags(os.Args[2:])
		if err != nil {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.Build(dir, out, os.Stdout))
	default:
		usage()
		os.Exit(1)
	}
}

func queryFlags(args []string) (id, name, pkg, kind string, err error) {
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return "", "", "", "", fmt.Errorf("flag")
		}
		switch args[i] {
		case "--id":
			i++
			id = args[i]
		case "--name":
			i++
			name = args[i]
		case "--pkg":
			i++
			pkg = args[i]
		case "--kind":
			i++
			kind = args[i]
		default:
			return "", "", "", "", fmt.Errorf("flag")
		}
	}
	return id, name, pkg, kind, nil
}

func buildFlags(args []string) (dir, out string, err error) {
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" {
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("-o")
			}
			i++
			out = args[i]
			continue
		}
		if dir != "" {
			return "", "", fmt.Errorf("dir")
		}
		dir = args[i]
	}
	if dir == "" || out == "" {
		return "", "", fmt.Errorf("need dir and -o")
	}
	return dir, out, nil
}

func usage() {
	fmt.Fprintf(os.Stdout, "{\"ok\":false,\"error\":\"usage\",\"detail\":\"ovid check <dir> | ovid query <dir> [--id s] [--name s] [--pkg s] [--kind s] | ovid patch <dir> <patch.json> | ovid build <dir> -o <file>\"}\n")
}
