//go:build !js

package urldriver

// brand describes one supported Chromium-family browser.
type brand struct {
	// binaryNames are candidate binary names tried in order on $PATH.
	binaryNames []string
	// userDataDir is the standard per-user data directory (relative to $HOME)
	// the brand uses when launched normally from the shell. We point
	// gridwell at the same path so extensions, cookies, and login state
	// match the user's interactive Brave/Chrome sessions.
	userDataDir string
	// extraFlags are launcher CLI flags specific to this brand.
	extraFlags []string
}

// brands enumerates the supported brand names accepted by --browser.
//
// binaryNames lists candidates that exec.LookPath will try in order. We
// include both PATH names (Linux: "google-chrome") and absolute macOS
// `.app` paths in the same list; LookPath accepts an absolute path
// directly when the name contains a slash, so darwin candidates fail
// harmlessly on Linux and vice versa. That keeps brand resolution OS-free.
var brands = map[string]brand{
	"chromium": {
		binaryNames: []string{
			"chromium", "chromium-browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		},
		userDataDir: ".config/chromium",
	},
	"chrome": {
		binaryNames: []string{
			"google-chrome", "google-chrome-stable", "chrome",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		},
		userDataDir: ".config/google-chrome",
	},
	"brave": {
		binaryNames: []string{
			"brave-browser", "brave",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		},
		userDataDir: ".config/BraveSoftware/Brave-Browser",
		extraFlags:  []string{"disable-brave-update"},
	},
	"edge": {
		binaryNames: []string{
			"microsoft-edge", "microsoft-edge-stable",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		},
		userDataDir: ".config/microsoft-edge",
	},
}

// BrandNames returns the supported brand names (for CLI usage messages).
func BrandNames() []string { return brandNames() }

func brandNames() []string {
	out := make([]string, 0, len(brands))
	for k := range brands {
		out = append(out, k)
	}
	return out
}
