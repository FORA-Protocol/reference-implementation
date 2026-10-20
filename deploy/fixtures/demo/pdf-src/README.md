# PDF sources for the Stoa Press demo essays

The two `.pdf` files under
`deploy/content/demo/stoa-press/articles/philosophers/pdf/` are generated from
the plain-text sources in this directory. The sources are the record of what
the PDFs say; the PDFs are the published content the origin serves.

Regenerate with the macOS built-in CUPS text-to-PDF filter (no extra
dependency; the output has a real text layer):

```
for s in xenophanes chrysippus; do
  cupsfilter -m application/pdf deploy/fixtures/demo/pdf-src/$s.txt \
    > deploy/content/demo/stoa-press/articles/philosophers/pdf/$s.pdf
done
```

On Linux, any text-to-PDF converter that keeps a text layer works the same
way (for example `enscript -p - $s.txt | ps2pdf - $s.pdf`). The bytes differ
between tools and runs (creation date, fonts); only the text matters.
