package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

// presentation is local to a command so embedded callers can use separate writers.
type presentation struct{ color, icons bool }

func (g *globalFlags) presentation(cfg *config.Config, out io.Writer) presentation {
	terminal := false
	if f, ok := out.(interface{ Fd() uintptr }); ok {
		terminal = term.IsTerminal(int(f.Fd()))
	}
	return outputPresentation(cfg.Output, g.plain, terminal)
}

func outputPresentation(opts config.Output, plain, terminal bool) presentation {
	if plain || opts.Plain {
		return presentation{}
	}
	auto := terminal && os.Getenv("CI") == "" && os.Getenv("TERM") != "dumb"
	p := presentation{color: auto, icons: auto}
	if opts.Color != nil {
		p.color = *opts.Color
	}
	if opts.Icons != nil {
		p.icons = *opts.Icons
	}
	if os.Getenv("NO_COLOR") != "" {
		p.color = false
	}
	return p
}

func (p presentation) paint(code, s string) string {
	if !p.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p presentation) icon(icon, s string) string {
	if !p.icons {
		return s
	}
	return icon + " " + s
}
func (p presentation) stack(s string) string { return p.paint("1;36", p.icon("\uf1b3", s)) }
func (p presentation) server(name, hostOS, distro string) string {
	icon := "\uf233"
	switch strings.ToLower(hostOS) {
	case "linux":
		icon = "\uf17c"
		if distroIcon, ok := linuxIcons[distro]; ok {
			icon = distroIcon
		}
	case "darwin":
		icon = "\uf179"
	case "freebsd":
		icon = "\uf30c"
	case "windows", "windows_nt":
		icon = "\uf17a"
	}
	return p.icon(icon, name)
}

// Keys are exact os-release IDs: an unrecognized derivative keeps the Linux
// fallback rather than incorrectly displaying its parent distribution's logo.
var linuxIcons = map[string]string{
	"ubuntu":              "\uf31b",
	"debian":              "\uf306",
	"alpine":              "\uf300",
	"arch":                "\uf303",
	"fedora":              "\uf30a",
	"centos":              "\uf304",
	"rhel":                "\uf316",
	"linuxmint":           "\uf30e",
	"opensuse":            "\uf314",
	"opensuse-leap":       "\uf314",
	"opensuse-tumbleweed": "\uf314",
	"nixos":               "\uf313",
	"gentoo":              "\uf30d",
	"rocky":               "\uf32b",
	"almalinux":           "\uf31d",
	"raspbian":            "\uf315",
	"kali":                "\uf327",
}

func (p presentation) state(s string) string {
	switch s {
	case deploy.StateRunning:
		return p.paint("32", p.icon("\uf058", s))
	case deploy.StateStarting, deploy.StateDegraded:
		return p.paint("33", p.icon("\uf071", s))
	case deploy.StateUnhealthy, deploy.StateUnreachable:
		return p.paint("31", p.icon("\uf057", s))
	default:
		return p.paint("90", p.icon("\uf28d", s))
	}
}

// table measures terminal cells, excluding ANSI escapes and accounting for
// Nerd Font glyphs. text/tabwriter counts escapes as visible characters.
type table struct{ rows [][]string }

func (t *table) row(cells ...string) { t.rows = append(t.rows, cells) }
func (t *table) print(out io.Writer) error {
	var widths []int
	for _, row := range t.rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if n := ansi.StringWidth(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if _, err := io.WriteString(out, cell); err != nil {
				return err
			}
			if i < len(row)-1 {
				if _, err := io.WriteString(out, strings.Repeat(" ", widths[i]-ansi.StringWidth(cell)+2)); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}
func (p presentation) header(t *table, cells ...string) {
	for i := range cells {
		cells[i] = p.paint("1;90", cells[i])
	}
	t.row(cells...)
}
