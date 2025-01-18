package common

import (
	"fmt"
	"os"
)

func IsDirectory(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}

	if !stat.IsDir() {
		return fmt.Errorf("not a directory")
	}

	return nil
}
