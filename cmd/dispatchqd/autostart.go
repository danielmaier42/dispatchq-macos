package main

import "fmt"

// valueDeleter is the part of an open registry key removeAutostart needs.
type valueDeleter interface {
	DeleteValue(name string) error
	Close() error
}

// removeAutostart deletes the autostart value name from the key open returns.
// A key or value that does not exist is already the goal and counts as done
// (isMissing decides which errors mean that); any other failure, such as
// access denied on the key, is returned so the caller does not report the
// autostart entry as removed while Windows keeps starting the daemon.
func removeAutostart(open func() (valueDeleter, error), isMissing func(error) bool, name string) error {
	k, err := open()
	if err != nil {
		if isMissing(err) {
			return nil
		}
		return fmt.Errorf("open the Run key: %w", err)
	}
	defer func() { _ = k.Close() }()
	if err := k.DeleteValue(name); err != nil && !isMissing(err) {
		return fmt.Errorf("remove autostart: %w", err)
	}
	return nil
}
