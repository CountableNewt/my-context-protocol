package compiler

import (
	"fmt"
	"strings"
)

// BodyDiff ports the bounded line-oriented release diff (not a patch format).
func BodyDiff(oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.TrimSuffix(s, "\n")
		return strings.Split(s, "\n")
	}
	a, b := lines(oldText), lines(newText)
	header := "--- prior release\n+++ new commit\n"
	if len(a) > 2500 || len(b) > 2500 {
		var out strings.Builder
		out.WriteString(header)
		fmt.Fprintf(&out, "(Large file: %d vs %d lines — showing first changes only)\n", len(a), len(b))
		shown := 0
		count := len(a)
		if len(b) > count {
			count = len(b)
		}
		for i := 0; i < count; i++ {
			o, n := "", ""
			if i < len(a) {
				o = a[i]
			}
			if i < len(b) {
				n = b[i]
			}
			if o != n {
				fmt.Fprintf(&out, "-%s\n+%s\n", o, n)
				shown++
			}
			if shown >= 60 || out.Len() >= 60000 {
				out.WriteString("\n… truncated …\n")
				break
			}
		}
		return out.String()
	}
	width := len(b) + 1
	dp := make([]uint16, (len(a)+1)*width)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i*width+j] = dp[(i-1)*width+j-1] + 1
			} else {
				left, up := dp[i*width+j-1], dp[(i-1)*width+j]
				if left > up {
					dp[i*width+j] = left
				} else {
					dp[i*width+j] = up
				}
			}
		}
	}
	script := []string{}
	i, j := len(a), len(b)
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			script = append(script, " "+a[i-1]+"\n")
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i*width+j-1] >= dp[(i-1)*width+j]) {
			script = append(script, "+"+b[j-1]+"\n")
			j--
		} else {
			script = append(script, "-"+a[i-1]+"\n")
			i--
		}
	}
	var out strings.Builder
	out.WriteString(header)
	bodyLength := 0
	for i := len(script) - 1; i >= 0; i-- {
		out.WriteString(script[i])
		bodyLength += len([]rune(script[i]))
		if bodyLength >= 60000 {
			out.WriteString("\n… diff truncated …\n")
			break
		}
	}
	return out.String()
}
