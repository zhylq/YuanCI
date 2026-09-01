package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yuanci/yuanci/internal/pipeline"
)

func main() {
	if handled, err := runnerPKICommand(os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if handled, err := adminCommand(os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	usage := func() { fmt.Fprintln(os.Stderr, "usage: yuancictl validate [-file .yuanci.yml]") }
	if len(os.Args) < 2 || os.Args[1] != "validate" {
		usage()
		os.Exit(2)
	}
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	file := flags.String("file", ".yuanci.yml", "pipeline configuration path")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		usage()
		os.Exit(2)
	}
	content, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read pipeline:", err)
		os.Exit(1)
	}
	plan, err := pipeline.Compile(content, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid pipeline:", err)
		os.Exit(1)
	}
	fmt.Printf("valid pipeline %q (%s)\n", plan.Name, plan.ConfigSHA256)
}
