// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (BSD-3-Clause, © CESNET).

package compile

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"
)

// pathMax is PATH_MAX: libyang's opendir fails on longer directory paths, which
// only logs a warning without a context (not stored), so such a directory is skipped.
const pathMax = 4096

// file is a (sub)module file found by search.
type file struct {
	fsys fs.FS
	name string // path inside fsys
	yin  bool
}

// search is lys_search_localfile (without the implicit current directory,
// LY_CTX_DISABLE_SEARCHDIR_CWD): directories are a stack, the last search
// directory and the last found subdirectory are searched first. Symlinks are
// followed (fs.Stat), so a symlink cycle is bounded by Budget.MaxSearchDirs
// (U-0022). It returns a nil file when nothing matches.
func (c *Context) search(name, rev string) (*file, error) {
	type dir struct {
		fsys fs.FS
		name string
	}
	var dirs []dir
	for _, d := range c.dirs {
		dirs = append(dirs, dir{d, "."})
	}
	var match *file
	matchRev := "" // file name part after name ("@..." or ".…") of match
	opened := 0
	for len(dirs) > 0 {
		wd := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		if len(wd.name) > pathMax {
			continue
		}
		if opened++; opened > c.opts.MaxSearchDirs {
			return nil, fmt.Errorf("%w: more than %d directories searched for module %q", ErrBudget,
				c.opts.MaxSearchDirs, name)
		}
		ents, err := fs.ReadDir(wd.fsys, wd.name)
		if err != nil {
			continue // LOGWRN(NULL, ...), not stored
		}
		for _, e := range ents {
			p := path.Join(wd.name, e.Name())
			isDir, isReg := e.IsDir(), e.Type().IsRegular()
			if e.Type()&fs.ModeSymlink != 0 { // lys_search_localfile_file_type: stat the target
				if st, err := fs.Stat(wd.fsys, p); err == nil {
					isDir, isReg = st.IsDir(), st.Mode().IsRegular()
				}
			}
			if isDir {
				dirs = append(dirs, dir{wd.fsys, p})
				continue
			}
			fn := e.Name()
			if !isReg || !strings.HasPrefix(fn, name) || len(fn) == len(name) || fn[len(name)] != '.' && fn[len(name)] != '@' {
				continue
			}
			var yin bool
			switch {
			case len(fn) >= len(".yang")+1 && strings.HasSuffix(fn, ".yang"):
			case len(fn) >= len(".yin")+1 && strings.HasSuffix(fn, ".yin"):
				yin = true
			default:
				continue
			}
			rest := fn[len(name):]
			if rev != "" {
				if rest[0] == '@' {
					if strings.HasPrefix(rest[1:], rev) { // exact revision
						return &file{wd.fsys, p, yin}, nil
					}
					continue
				}
				match, matchRev = &file{wd.fsys, p, yin}, rest // fallback without revision
				continue
			}
			if match != nil {
				suffix := ".yang"
				if yin {
					suffix = ".yin"
				}
				if rest[0] != '@' || !validDate(strings.TrimSuffix(rest[1:], suffix)) {
					continue
				}
				if matchRev[0] == '@' && strncmp(matchRev[1:], rest[1:], 10) >= 0 {
					continue
				}
			}
			match, matchRev = &file{wd.fsys, p, yin}, rest
		}
	}
	return match, nil
}

// validDate is lys_check_date without logging.
func validDate(d string) bool {
	if len(d) != 10 {
		return false
	}
	for i := range len(d) {
		if i == 4 || i == 7 {
			if d[i] != '-' {
				return false
			}
		} else if d[i] < '0' || d[i] > '9' {
			return false
		}
	}
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// strncmp compares at most n bytes like C strncmp (a shorter string is smaller).
func strncmp(a, b string, n int) int {
	return strings.Compare(a[:min(n, len(a))], b[:min(n, len(b))])
}
