# Recorded readability evidence

These are actual embedded `render.html`/Chrome captures, not reports or PNGs painted from expected geometry by a test fake. Normal unit runs only load and hash-check them, copying artifacts to temporary absolute paths.

- `d100827…`: readable branch/loop/crossing scene; expected to pass with one advisory crossing.
- `edbfa6c1…`: same scene plus an independent parallel route; expected to pass.
- `13781e34…`: deliberately merged arrivals at node `b`; expected to block ambiguous attachment/merged lines.

Each directory is keyed by `layout.GeometryDigest` and contains candidate geometry, the original DOM report/artifact hashes, real measured text, bundled-font coverage fingerprint/embedded WOFF2 hashes, an overview and every overlapping practical-zoom tile. The scene's loop uses bottom/top ports, as required by the shared geometry validator.

Regenerate explicitly from the repository root (requires Chrome/Chromium):

```sh
make ui
ARBITRON_UPDATE_RENDER_FIXTURES=1 ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run '^TestGenerateReadabilityFixture$' -count=1 -v
```

Fresh browser tests independently exercise the coordinator, sample workflow, Unicode clipping, actual CSS/SVG mutations, crossing/seam capture and both immutable retry captures. Recordings alone are not release/root acceptance.
