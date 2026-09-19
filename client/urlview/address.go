package urlview

// Address is where a url tile's view opens. A tile whose plugin serves its
// page carries no url_string — the node derives the /content/ address instead
// — so the served page is read first: reading the row first would open the
// empty string, which is a blank view and no error anywhere.
func Address(page bool, pageURL, urlString string) string {
	if page {
		return pageURL
	}
	return urlString
}
