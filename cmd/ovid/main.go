package main

import (
	"fmt"
	"os"
	"strconv"

	"ovid/internal/query"
	"ovid/internal/tool"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "init":
		if len(os.Args) != 3 {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.Init(os.Args[2], os.Stdout))
	case "check":
		if len(os.Args) < 3 || len(os.Args) > 4 || (len(os.Args) == 4 && os.Args[3] != "--facts") {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.CheckOptions(os.Args[2], len(os.Args) == 4, os.Stdout))
	case "query":
		if len(os.Args) < 3 {
			usage()
			os.Exit(1)
		}
		opts, err := queryFlags(os.Args[3:])
		if err != nil {
			usage()
			os.Exit(1)
		}
		os.Exit(tool.QueryOptions(os.Args[2], opts, os.Stdout))
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

func queryFlags(args []string) (query.Options, error) {
	opts := query.DefaultOptions()
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == "--full" {
			opts.Full = true
			continue
		}
		if i+1 >= len(args) || args[i+1] == "" {
			return opts, fmt.Errorf("%s requires a value", flag)
		}
		i++
		value := args[i]
		switch flag {
		case "--id":
			opts.ID = value
		case "--name":
			opts.Name = value
		case "--pkg":
			opts.Pkg = value
		case "--kind":
			opts.Kind = value
		case "--calls-to":
			opts.CallsTo = value
		case "--limit", "--offset":
			for _, c := range value {
				if c < '0' || c > '9' {
					return opts, fmt.Errorf("%s requires a nonnegative integer", flag)
				}
			}
			n, err := strconv.ParseUint(value, 10, 31)
			if err != nil {
				return opts, fmt.Errorf("%s requires an integer between 0 and 2147483647", flag)
			}
			if flag == "--limit" {
				opts.Limit = int(n)
			} else {
				opts.Offset = int(n)
			}
		default:
			return opts, fmt.Errorf("unknown flag %s", flag)
		}
	}
	return opts, nil
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
	fmt.Fprintf(os.Stdout, "{\"ok\":false,\"error\":\"usage\",\"detail\":\"ovid init <dir> | ovid check <dir> [--facts] | ovid query <dir> [--id s] [--name s] [--pkg s] [--kind s] [--calls-to ID] [--full] [--limit N] [--offset N] | ovid patch <dir> <patch.json> | ovid build <dir> -o <file>\"}\n")
}
