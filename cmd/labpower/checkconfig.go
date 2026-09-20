package main

import "fmt"

func runCheckConfig(args []string) error {
	fs, path := configFlagSet("check-config")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	if _, err := loadValidConfig(*path); err != nil {
		return err
	}
	fmt.Printf("%s: OK\n", *path)
	return nil
}
