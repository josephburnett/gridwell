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
var brands = map[string]brand{
	"chromium": {
		binaryNames: []string{"chromium", "chromium-browser"},
		userDataDir: ".config/chromium",
	},
	"chrome": {
		binaryNames: []string{"google-chrome", "google-chrome-stable", "chrome"},
		userDataDir: ".config/google-chrome",
	},
	"brave": {
		binaryNames: []string{"brave-browser", "brave"},
		userDataDir: ".config/BraveSoftware/Brave-Browser",
		extraFlags:  []string{"disable-brave-update"},
	},
	"edge": {
		binaryNames: []string{"microsoft-edge", "microsoft-edge-stable"},
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
