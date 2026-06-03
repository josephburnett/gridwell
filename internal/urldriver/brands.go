//go:build !js

package urldriver

// brand describes one supported Chromium-family browser. The profile
// directory is *not* a per-brand field anymore — every brand stores its
// profile under ~/.gridwell/profiles/<brand-name>, a gridwell-owned
// location separate from the OS's standard browser profile path. That
// avoids Chrome 136+'s automation block (which only applies to the
// default profile) and the SingletonLock conflict from a user's
// interactive browser.
type brand struct {
	// binaryNames are candidate binary names tried in order on $PATH.
	binaryNames []string
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
	},
	"chrome": {
		binaryNames: []string{
			"google-chrome", "google-chrome-stable", "chrome",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		},
	},
	"brave": {
		binaryNames: []string{
			"brave-browser", "brave",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		},
		extraFlags: []string{"disable-brave-update"},
	},
	"edge": {
		binaryNames: []string{
			"microsoft-edge", "microsoft-edge-stable",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		},
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
