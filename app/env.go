package app

import (
	"io/ioutil"
	"net/url"
	"os"
	"regexp"
	"strconv"
)

type ErrMissingEnvVar string

func (name ErrMissingEnvVar) Error() string {
	return "missing environment variable: " + string(name)
}

func requireEnv(name string) (string, error) {
	if val, ok := os.LookupEnv(name); ok {
		return val, nil
	}

	return "", ErrMissingEnvVar(name)
}

func lookupInt(name string, def int) (int, error) {
	if val, ok := os.LookupEnv(name); ok {
		return strconv.Atoi(val)
	}

	return def, nil
}

func lookupBool(name string, def bool) (bool, error) {
	if val, ok := os.LookupEnv(name); ok {
		return regexp.MatchString("^(?i:t|true|yes)$", val)
	}

	return def, nil
}

func lookupURL(name string) (*url.URL, error) {
	if val, ok := os.LookupEnv(name); ok {
		return url.Parse(val)
	}
	return nil, nil
}

func lookupSecureURL(name string) (*url.URL, error) {
	if fileName, ok := os.LookupEnv(name); ok {
		val, err := ioutil.ReadFile(fileName)
		if err != nil {
			return nil, err
		}
		return url.Parse(string(val))
	}
	return nil, nil
}
