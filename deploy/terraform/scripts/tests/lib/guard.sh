# Helpers the shell guard tests in this directory share. Sourced, not run.

# mode FILE — FILE's permission bits in octal, on GNU and BSD stat alike.
mode() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }
