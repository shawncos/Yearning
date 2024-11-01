package lib

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

// 以太坊密钥账户是 eth account: 0xc1912fEE45d61C87Cc5EA59DaE31190FFFFf232d 私钥
// GetFingerprint gets mysql query fingerprint.
// From https://github.com/bytebase/bytebase/blob/c82e4864222bb20a5e26060e1e3a28368a035c7c/backend/plugin/parser/mysql/fingerprint.go#L11
// From https://github.com/percona/percona-toolkit/blob/af686fe186d1fca4c4392c8fa75c31a00c8fb273/bin/pt-query-digest#L2930
func GetFingerprint(query string) (string, error) {
	// Match SQL queries generated by mysqldump command.
	if matched, _ := regexp.MatchString(`\ASELECT /\*!40001 SQL_NO_CACHE \*/ \* FROM `, query); matched {
		return "mysqldump", nil
	}
	// Match SQL queries generated by Percona Toolkit.
	if matched, _ := regexp.MatchString(`/\*\w+\.\w+:[0-9]/[0-9]\*/`, query); matched {
		return "percona-toolkit", nil
	}
	// Match administrator commands.
	if matched, _ := regexp.MatchString(`\Aadministrator command: `, query); matched {
		return query, nil
	}
	// Match stored procedure call statements.
	if matched, _ := regexp.MatchString(`\A\s*(call\s+\S+)\(`, query); matched {
		return strings.ToLower(regexp.MustCompile(`\A\s*(call\s+\S+)\(`).FindStringSubmatch(query)[1]), nil
	}
	// Match INSERT INTO or REPLACE INTO statements.
	if beginning := regexp.MustCompile(`(?i)((?:INSERT|REPLACE)(?: IGNORE)?\s+INTO.+?VALUES\s*\(.*?\))\s*,\s*\(`).FindStringSubmatch(query); len(beginning) > 0 {
		query = beginning[1]
	}

	// Match multi-line comments and single-line comments, and remove them.
	mlcRe := regexp.MustCompile(`(?s)/\*.*?\*/`)
	olcRe := regexp.MustCompile(`(?m)--.*$`)
	query = mlcRe.ReplaceAllString(query, "")
	query = olcRe.ReplaceAllString(query, "")

	// Replace the database name in USE statements with a question mark (?).
	query = regexp.MustCompile(`(?i)\Ause \S+\z`).ReplaceAllString(query, "use ?")

	// Replace escape characters and special characters in SQL queries with a question mark (?).
	query = regexp.MustCompile(`([^\\])(\\')`).ReplaceAllString(query, "$1")
	query = regexp.MustCompile(`([^\\])(\\")`).ReplaceAllString(query, "$1")
	query = regexp.MustCompile(`\\\\`).ReplaceAllString(query, "")
	query = regexp.MustCompile(`\\'`).ReplaceAllString(query, "")
	query = regexp.MustCompile(`\\"`).ReplaceAllString(query, "")
	query = regexp.MustCompile(`([^\\])(".*?[^\\]?")`).ReplaceAllString(query, "$1?")
	query = regexp.MustCompile(`([^\\])('.*?[^\\]?')`).ReplaceAllString(query, "$1?")

	// Replace boolean values in SQL queries with a question mark (?).
	query = regexp.MustCompile(`\bfalse\b|\btrue\b`).ReplaceAllString(query, "?")

	// Replace MD5 values in SQL queries with a question mark (?).
	if matched, _ := regexp.MatchString(`([._-])[a-f0-9]{32}`, query); matched {
		query = regexp.MustCompile(`([._-])[a-f0-9]{32}`).ReplaceAllString(query, "$1?")
	}

	// Replace numbers in SQL queries with a question mark (?).
	if matched, _ := regexp.MatchString(`\b[0-9+-][0-9a-f.xb+-]*`, query); matched {
		query = regexp.MustCompile(`\b[0-9+-][0-9a-f.xb+-]*`).ReplaceAllString(query, "?")
	}

	// Replace special characters in SQL queries with a question mark (?).
	if matched, _ := regexp.MatchString(`[xb+-]\?`, query); matched {
		query = regexp.MustCompile(`[xb+-]\?`).ReplaceAllString(query, "?")
	} else {
		query = regexp.MustCompile(`[xb.+-]\?`).ReplaceAllString(query, "?")
	}

	// Remove spaces and line breaks in SQL queries.
	query = strings.TrimSpace(query)
	query = strings.TrimRight(query, "\n\r\f ")
	query = regexp.MustCompile(`\s+`).ReplaceAllString(query, " ")
	query = strings.ToLower(query)

	// Replace NULL values in SQL queries with a question mark (?).
	query = regexp.MustCompile(`\bnull\b`).ReplaceAllString(query, "?")

	// Replace IN and VALUES clauses in SQL queries with a question mark (?).
	query = regexp.MustCompile(`\b(in|values?)(?:[\s,]*\([\s?,]*\))+`).ReplaceAllString(query, "$1(?+)")

	var err error
	query, err = collapseUnion(query)
	if err != nil {
		return "", err
	}

	// Replace numbers in the LIMIT clause of SQL queries with a question mark (?).
	query = regexp.MustCompile(`\blimit \?(?:, ?\?| offset \?)?`).ReplaceAllString(query, "limit ?")

	// Remove ASC sorting in SQL queries.
	if matched, _ := regexp.MatchString(`\border by `, query); matched {
		ascRegexp := regexp.MustCompile(`(.+?)\s+asc`)
		for {
			if matched := ascRegexp.MatchString(query); matched {
				query = ascRegexp.ReplaceAllString(query, "$1")
			} else {
				break
			}
		}
	}

	return query, nil
}

func collapseUnion(query string) (string, error) {
	// The origin perl code is:
	//   $query =~ s{                          # Collapse UNION
	//     \b(select\s.*?)(?:(\sunion(?:\sall)?)\s\1)+
	//	  }
	//	  {$1 /*repeat$2*/}xg;
	// But Golang doesn't support \1(back-reference).
	// So we use the following code to replace it.
	unionRegexp := regexp.MustCompile(`\s(union all|union)\s`)
	parts := unionRegexp.Split(query, -1)
	if len(parts) == 1 {
		return query, nil
	}
	// Add a sentinel node to the end of the slice.
	// Because we remove all comments before, so all parts are different from sentinel node.
	parts = append(parts, "/*Sentinel Node*/")
	separators := unionRegexp.FindAllString(query, -1)
	if len(parts) != len(separators)+2 {
		return "", errors.Errorf("find %d parts, but %d separators", len(parts)-1, len(separators))
	}
	start := 0
	var buf bytes.Buffer
	if _, err := buf.WriteString(parts[start]); err != nil {
		return "", err
	}
	for i, part := range parts {
		if i == 0 {
			continue
		}
		if part == parts[start] {
			continue
		}
		if i == start+1 {
			// The i-th part is not equal to the front part.
			if _, err := buf.WriteString(separators[i-1]); err != nil {
				return "", err
			}
		} else {
			// deal with the same parts[start, i-1] and start < i-1.
			if _, err := buf.WriteString(" /*repeat"); err != nil {
				return "", err
			}
			// Write the last separator between the same parts[start, i-1].
			// In other words, the last separator is the separator between the i-th part and the (i-1)-th part.
			// So the index of the last separator is (i-1)-1.
			if _, err := buf.WriteString(separators[(i-1)-1]); err != nil {
				return "", err
			}
			if _, err := buf.WriteString("*/"); err != nil {
				return "", err
			}
		}
		start = i
		// Don't write the sentinel node.
		if i != len(parts)-1 {
			if _, err := buf.WriteString(parts[start]); err != nil {
				return "", err
			}
		}
	}
	return buf.String(), nil
}
