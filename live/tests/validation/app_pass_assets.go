package validation

import "embed"

// appPassAssets embeds the three throwaway media fixtures AppPass uploads in stage 4
// (a 64x64 PNG, a 1-page PDF, and a short H.264 MP4). Kept in their own directory
// (app_pass_assets/), separate from probe_assets/ which dr_probe.go walks wholesale
// into the DR-drill Lambda zip — these files must never end up in that zip.
//
//go:embed app_pass_assets/app_pass_probe.png app_pass_assets/app_pass_probe.pdf app_pass_assets/app_pass_probe.mp4
var appPassAssets embed.FS
