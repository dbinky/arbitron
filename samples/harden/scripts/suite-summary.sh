#!/bin/sh
# Summarizes a failed test-suite or mutation-tool log in under 3.5KB.
#
#   suite-summary.sh <log>
#
# Prints the failing test names (Go `--- FAIL:` and `FAIL <pkg>` lines, pytest
# `FAILED`, cargo `test ... FAILED`, Jest `●`, dotnet `Failed`), then the first
# 40 lines of the first failure (Go: from the first `--- FAIL:`, else the first
# `panic:`). With no failure markers it prints the log's last 40 lines instead.
# Meant to lead a node error, which the engine keeps only the first 1MB of: a
# sandbox-only test failure otherwise sits at the end of a truncated log.
# Always exits 0.
LOG="${1:-}"
MAX=3400

if [ -z "$LOG" ] || [ ! -s "$LOG" ]; then
  echo "suite summary: no log at '${LOG}'"
  exit 0
fi

awk -v max="$MAX" '
  function keep(s) { if (length(s) > 240) s = substr(s, 1, 239) "…"; return s }
  {
    line = $0; sub(/\r$/, "", line); lines[NR] = line
    is_name = (line ~ /^[ \t]*--- FAIL: / || line ~ /^FAIL[ \t]+[^ \t]/ ||
               line ~ /^FAILED / || line ~ /^test .* \.\.\. FAILED$/ ||
               line ~ /^[ \t]*● / || line ~ /^[ \t]*Failed [A-Za-z_]/)
    if (is_name && !(line in seen)) { seen[line] = 1; names[++nn] = line }
    if (!start && (line ~ /^[ \t]*--- FAIL: / || line ~ /^____+ .* ____+$/ ||
                   line ~ /^---- .* stdout ----$/ || line ~ /^[ \t]*● / ||
                   line ~ /^[ \t]*Failed [A-Za-z_]/ || line ~ /^FAILED /)) start = NR
    if (!panic && line ~ /^panic: /) panic = NR
  }
  END {
    if (!start) start = panic
    out = ""
    if (nn == 0 && !start) {
      out = "suite summary: no test failure lines found; the last 40 lines of the log:\n"
      from = NR - 39; if (from < 1) from = 1
      for (i = from; i <= NR; i++) out = out "  " keep(lines[i]) "\n"
    } else {
      out = "suite summary: " nn " failing test line(s):\n"
      for (i = 1; i <= nn; i++) {
        add = "  " keep(names[i]) "\n"
        if (length(out) + length(add) > max / 2) { out = out "  ... and " (nn - i + 1) " more\n"; break }
        out = out add
      }
      if (start) {
        out = out "first failure (first 40 lines):\n"
        for (i = start; i < start + 40 && i <= NR; i++) {
          # The failure ends at the next passing package or top-level failure.
          if (i > start && (lines[i] ~ /^ok[ \t]/ || lines[i] ~ /^--- FAIL: /)) break
          out = out "  " keep(lines[i]) "\n"
        }
      }
    }
    if (length(out) > max) out = substr(out, 1, max) "\n  ... (summary cut)\n"
    printf "%s", out
  }' "$LOG"
exit 0
