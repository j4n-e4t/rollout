package deploy

import "strings"

// Probe host metadata in the existing SSH session. Read os-release as data,
// never source it as shell code. The vendor file is a fallback for minimal hosts.
const hostProbe = `echo "@host"
uname -s 2>/dev/null || true
echo "@os-release"
if [ -r /etc/os-release ]; then
  cat /etc/os-release 2>/dev/null || true
elif [ -r /usr/lib/os-release ]; then
  cat /usr/lib/os-release 2>/dev/null || true
fi
echo
`

func parseHostInfo(out string) (hostOS, distro string) {
	section := ""
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "@") {
			section = line
			continue
		}
		switch section {
		case "@host":
			if hostOS == "" {
				hostOS = line
			}
		case "@os-release":
			key, value, ok := strings.Cut(line, "=")
			if !ok || key != "ID" {
				continue
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = value[1 : len(value)-1]
			}
			// os-release IDs contain only lowercase letters, digits, dots, underscores
			// and hyphens. Ignore malformed values rather than passing them to the UI.
			valid := value != ""
			for _, r := range value {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
					valid = false
					break
				}
			}
			if valid {
				distro = value
			}
		}
	}
	return hostOS, distro
}
