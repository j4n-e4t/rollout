package deploy

import (
	"strconv"
	"strings"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/remote"
)

// File is a deployed file of a service, relative to its deploy directory.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"` // -1 if unknown
}

// Files lists up to limit deployed files of svc on srv, sorted by path, and
// the total count. Deploys replace everything in the directory except
// preserved entries, so these are exactly the shipped files; preserved
// entries (persistent data) are skipped.
func Files(ssh *remote.SSH, srv *config.Server, svc *config.Service, limit int) ([]File, int, error) {
	out, err := ssh.Output(Target(srv), "sh -c "+shellQuote(filesScript(srv.DeployPath+"/"+svc.Name, svc.Preserve, limit)))
	if err != nil {
		return nil, 0, err
	}
	files, total := parseFiles(string(out))
	return files, total, nil
}

func filesScript(dir string, preserve []string, limit int) string {
	prune := ""
	if len(preserve) > 0 {
		var paths []string
		for _, p := range preserve {
			paths = append(paths, "-path "+shellQuote("./"+p))
		}
		prune = `\( ` + strings.Join(paths, " -o ") + ` \) -prune -o `
	}
	return `cd ` + shellQuote(dir) + ` 2>/dev/null || exit 0
find . ` + prune + `\( -type f -o -type l \) -print | LC_ALL=C sort | {
  i=0
  while IFS= read -r f; do
    i=$((i+1))
    [ "$i" -le ` + strconv.Itoa(limit) + ` ] || continue
    printf '%s\t%s\n' "$(wc -c < "$f" 2>/dev/null || echo -1)" "${f#./}"
  done
  echo "@total $i"
}
`
}

func parseFiles(out string) ([]File, int) {
	files := []File{}
	total := 0
	for line := range strings.SplitSeq(out, "\n") {
		if n, ok := strings.CutPrefix(line, "@total "); ok {
			total, _ = strconv.Atoi(strings.TrimSpace(n))
			continue
		}
		size, path, ok := strings.Cut(line, "\t")
		if !ok || path == "" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
		if err != nil {
			n = -1
		}
		files = append(files, File{Path: path, Size: n})
	}
	return files, max(total, len(files))
}
