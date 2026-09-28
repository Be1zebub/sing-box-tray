//go:build windows

package tray

import (
	"os"
	"path/filepath"
)

const yacdBootFile = "boot.html"

// yacdBootHTML stores the Clash API secret in YACD's own localStorage key
// and then opens /ui/ with an empty query. The query is read here, on a file
// Go's file server does not redirect and YACD's service worker does not claim.
const yacdBootHTML = `<!DOCTYPE html>
<meta charset="utf-8">
<title>YACD</title>
<script>
(function () {
  var q = new URLSearchParams(location.search);
  var host = q.get("hostname") || "127.0.0.1";
  var port = q.get("port") || "9090";
  var secret = q.get("secret") || "";
  var base = "http://" + (host.indexOf(":") >= 0 ? "[" + host + "]" : host) + ":" + port;
  var key = "yacd.metacubex.one";
  var state = {};
  try { state = JSON.parse(localStorage.getItem(key) || "{}") || {}; } catch (e) { state = {}; }
  if (!Array.isArray(state.clashAPIConfigs)) state.clashAPIConfigs = [];
  var cfg = { baseURL: base, secret: secret, addedAt: Date.now() };
  var idx = -1;
  for (var i = 0; i < state.clashAPIConfigs.length; i++) {
    var item = state.clashAPIConfigs[i];
    if (item && item.baseURL === cfg.baseURL && item.secret === cfg.secret) { idx = i; break; }
  }
  if (idx < 0) { state.clashAPIConfigs.push(cfg); idx = state.clashAPIConfigs.length - 1; }
  state.selectedClashAPIConfigIndex = idx;
  localStorage.setItem(key, JSON.stringify(state));
  location.replace("/ui/");
})();
</script>
`

// installYacdBoot writes boot.html into the directory sing-box serves as /ui/.
func installYacdBoot(dir string) error {
	return os.WriteFile(filepath.Join(dir, yacdBootFile), []byte(yacdBootHTML), 0o644)
}

// findYacdDir returns the external_ui directory ("yacd") sing-box is serving.
// The path in the config is relative to the process working directory; a
// double-clicked tray uses its own folder instead.
func (a *App) findYacdDir() (string, error) {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "yacd"))
	}
	dirs = append(dirs, filepath.Join(a.exeDir, "yacd"))
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir, nil
		}
	}
	return "", os.ErrNotExist
}
