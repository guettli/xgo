package flag

import (
	"fmt"
	"strings"
)

func TryParseFlagValue(flag string, pval *string, pi *int, args []string) (ok bool, err error) {
	i := *pi
	val, next, ok := tryParseArg(flag, args[i])
	if !ok {
		return false, nil
	}
	if next {
		if i+1 >= len(args) {
			return false, fmt.Errorf("flag %s requires value", args[i])
		}
		val = args[i+1]
		*pi++
	}
	*pval = val
	return true, nil
}

func TryParseFlagsValue(flags []string, pval *string, pi *int, args []string) (ok bool, err error) {
	for _, flag := range flags {
		ok, err := TryParseFlagValue(flag, pval, pi, args)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func tryParseArg(flag string, arg string) (value string, next bool, ok bool) {
	if !strings.HasPrefix(arg, flag) {
		return "", false, false
	}
	if len(arg) == len(flag) {
		return "", true, true
	}
	if arg[len(flag)] == '=' {
		return arg[len(flag)+1:], false, true
	}
	return "", false, false
}
