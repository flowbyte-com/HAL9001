package cliui

import (
	"fmt"
)

const (
	colorBlue = "\033[1;38;2;62;93;185m"
	colorRed  = "\033[1;38;2;213;70;70m"
	colorOff  = "\033[0m"

	// picoClawBanner is the large ASCII logo shown for the default brand.
	picoClawBanner = "\r\n" +
		colorBlue + "██████╗ ██╗ ██████╗ ██████╗ " + colorRed + " ██████╗██╗      █████╗ ██╗    ██╗\n" +
		colorBlue + "██╔══██╗██║██╔════╝██╔═══██╗" + colorRed + "██╔════╝██║     ██╔══██╗██║    ██║\n" +
		colorBlue + "██████╔╝██║██║     ██║   ██║" + colorRed + "██║     ██║     ███████║██║ █╗ ██║\n" +
		colorBlue + "██╔═══╝ ██║██║     ██║   ██║" + colorRed + "██║     ██║     ██╔══██║██║███╗██║\n" +
		colorBlue + "██║     ██║╚██████╗╚██████╔╝" + colorRed + "╚██████╗███████╗██║  ██║╚███╔███╔╝\n" +
		colorBlue + "╚═╝     ╚═╝ ╚═════╝ ╚═════╝ " + colorRed + " ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝\n " +
		colorOff + "\r\n"

	// picoClawPlainBanner is the same logo without ANSI colors.
	picoClawPlainBanner = "\r\n" +
		"██████╗ ██╗ ██████╗ ██████╗  ██████╗██╗      █████╗ ██╗    ██╗\n" +
		"██╔══██╗██║██╔════╝██╔═══██╗██╔════╝██║     ██╔══██╗██║    ██║\n" +
		"██████╔╝██║██║     ██║   ██║██║     ██║     ███████║██║ █╗ ██║\n" +
		"██╔═══╝ ██║██║     ██║   ██║██║     ██║     ██╔══██║██║███╗██║\n" +
		"██║     ██║╚██████╗╚██████╔╝╚██████╗███████╗██║  ██║╚███╔███╔╝\n" +
		"╚═╝     ╚═╝ ╚═════╝ ╚═════╝  ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝\n " +
		"\r\n"

	// halEye is the small circular eye shown next to the HAL9001 heading.
	halEye = "●"
)

// RenderStartupBanner returns the startup banner for the active brand.
// The HAL9001 brand renders a compact heading (with a small red eye when
// color is available); every other brand keeps the PicoClaw ASCII logo.
// Nothing else is printed: no slogan, animation or decorative border.
func RenderStartupBanner(colorEnabled bool) string {
	b := CurrentBrand()
	if b.Compact {
		heading := b.Name
		if colorEnabled {
			heading = colorRed + halEye + " " + b.Name + colorOff
		} else {
			heading = halEye + " " + b.Name
		}
		return "\r\n" + heading + "\r\n"
	}
	if colorEnabled {
		return picoClawBanner
	}
	return picoClawPlainBanner
}

// PrintStartupBanner writes the startup banner for the active brand to
// stdout.
func PrintStartupBanner(colorEnabled bool) {
	fmt.Print(RenderStartupBanner(colorEnabled))
}

// PrintInteractiveNotice writes the one-line hint shown before the first
// prompt. The HAL9001 brand keeps it minimal: no logo, no slogan.
func PrintInteractiveNotice() {
	b := CurrentBrand()
	if b.Compact {
		fmt.Println("Ctrl+C to exit")
		fmt.Println()
		return
	}
	fmt.Printf("%s Interactive mode (Ctrl+C to exit)\n\n", b.Logo)
}
