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
type presentation struct {
	color, icons bool
	// width is the terminal's column count; 0 means unlimited (pipes, files).
	width int
}

func (g *globalFlags) presentation(cfg *config.Config, out io.Writer) presentation {
	terminal, width := false, 0
	if f, ok := out.(interface{ Fd() uintptr }); ok {
		terminal = term.IsTerminal(int(f.Fd()))
		if terminal {
			width, _, _ = term.GetSize(int(f.Fd()))
		}
	}
	p := outputPresentation(cfg.Output, g.plain, terminal)
	p.width = width
	return p
}

// fit cuts s to the terminal width so it never wraps.
func (p presentation) fit(s string) string {
	if p.width <= 0 || ansi.StringWidth(s) <= p.width {
		return s
	}
	return ansi.Truncate(s, p.width-1, "…")
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
// Styled output draws a rounded border; plain output stays aligned columns
// that grep and awk can read.
type table struct {
	rows   [][]string
	header bool
	p      presentation
}

func (p presentation) table(header ...string) *table {
	t := &table{p: p}
	if len(header) > 0 {
		for i := range header {
			header[i] = p.paint("1", header[i])
		}
		t.rows, t.header = [][]string{header}, true
	}
	return t
}

func (p presentation) bordered() bool { return p.color || p.icons }

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
	border := t.p.bordered()
	t.shrink(widths, border)
	var b strings.Builder
	rule := func(left, mid, right string) {
		var segs []string
		for _, w := range widths {
			segs = append(segs, strings.Repeat("─", w+2))
		}
		b.WriteString(t.p.paint("90", left+strings.Join(segs, mid)+right) + "\n")
	}
	if border {
		rule("╭", "┬", "╮")
	}
	for r, row := range t.rows {
		for i := range widths {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			if ansi.StringWidth(cell) > widths[i] {
				cell = ansi.Truncate(cell, widths[i]-1, "…")
			}
			pad := strings.Repeat(" ", widths[i]-ansi.StringWidth(cell))
			switch {
			case border:
				b.WriteString(t.p.paint("90", "│") + " " + cell + pad + " ")
			case i < len(widths)-1:
				b.WriteString(cell + pad + "  ")
			default:
				b.WriteString(cell)
			}
		}
		if border {
			b.WriteString(t.p.paint("90", "│"))
		}
		b.WriteString("\n")
		if border && r == 0 && t.header && len(t.rows) > 1 {
			rule("├", "┼", "┤")
		}
	}
	if border {
		rule("╰", "┴", "╯")
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// shrink narrows the widest columns until the table fits the terminal, so
// long cells are cut with "…" instead of wrapping and breaking the layout.
func (t *table) shrink(widths []int, border bool) {
	if t.p.width <= 0 || len(widths) == 0 {
		return
	}
	total := 2 * (len(widths) - 1)
	if border {
		total = 3*len(widths) + 1
	}
	for _, w := range widths {
		total += w
	}
	for total > t.p.width {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 8 {
			return // too narrow to help; let it wrap
		}
		widths[widest]--
		total--
	}
}

// heading prints a section title.
func (p presentation) heading(out io.Writer, s string) {
	fmt.Fprintln(out, p.paint("1", s))
}

func (p presentation) dim(s string) string { return p.paint("90", s) }
