package main

import "fmt"

func runCheckConfig(args []string) error {
	fs, path := configFlagSet("check-config")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	cfg, err := loadValidConfig(*path)
	if err != nil {
		return err
	}
	warnings := cfg.Warnings()
	for _, w := range warnings {
		fmt.Printf("warning: %s\n", w)
	}
	if len(warnings) > 0 {
		fmt.Printf("%s: OK with %d warning(s)\n", *path, len(warnings))
		return nil
	}
	fmt.Printf("%s: OK\n", *path)
	return nil
}
