package common

import (
	"fmt"
	"os"
	"strconv"
)

const (
	DEBUG = "DEBUG"
)

func IsDebug() bool {
	if v, _ := strconv.ParseBool(os.Getenv(DEBUG)); v {
		return true
	}
	return false
}

func RequiredEnvVar(variables ...string) error {
	for _, variable := range variables {
		if value := os.Getenv(variable); value == "" {
			return fmt.Errorf("'%s' is not defined", variable)
		}
	}
	return nil
}
