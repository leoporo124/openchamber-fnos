package main

import (
	"regexp"
	"strconv"
	"strings"
)

var semverPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

type parsedSemver struct {
	Core [3]int
	Pre  []string
}

func parseSemver(value string) (parsedSemver, bool) {
	match := semverPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return parsedSemver{}, false
	}
	var result parsedSemver
	for i := range result.Core {
		var err error
		result.Core[i], err = strconv.Atoi(match[i+1])
		if err != nil || (len(match[i+1]) > 1 && match[i+1][0] == '0') {
			return parsedSemver{}, false
		}
	}
	if match[4] != "" {
		result.Pre = strings.Split(match[4], ".")
		for _, identifier := range result.Pre {
			if identifier == "" {
				return parsedSemver{}, false
			}
		}
	}
	return result, true
}

func CompareSemver(left, right string) int {
	a, okA := parseSemver(left)
	b, okB := parseSemver(right)
	if !okA || !okB {
		return strings.Compare(left, right)
	}
	for i := range a.Core {
		if a.Core[i] < b.Core[i] {
			return -1
		}
		if a.Core[i] > b.Core[i] {
			return 1
		}
	}
	if len(a.Pre) == 0 && len(b.Pre) > 0 {
		return 1
	}
	if len(b.Pre) == 0 && len(a.Pre) > 0 {
		return -1
	}
	for i := 0; i < len(a.Pre) || i < len(b.Pre); i++ {
		if i >= len(a.Pre) {
			return -1
		}
		if i >= len(b.Pre) {
			return 1
		}
		if a.Pre[i] == b.Pre[i] {
			continue
		}
		x, errX := strconv.Atoi(a.Pre[i])
		y, errY := strconv.Atoi(b.Pre[i])
		if errX == nil && errY == nil {
			if x < y {
				return -1
			}
			return 1
		}
		if errX == nil {
			return -1
		}
		if errY == nil {
			return 1
		}
		return strings.Compare(a.Pre[i], b.Pre[i])
	}
	return 0
}
