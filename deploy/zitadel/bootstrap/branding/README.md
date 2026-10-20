# Sign-in branding assets

The logo, icon and font of the two Zitadel instances' sign-in pages and emails.
The developer instance serves MCP developers; the publisher instance serves the
publisher console. The setup uploads these files; nothing is set by hand.

| File | Used for | Source | Licence |
|---|---|---|---|
| `fora-mark-black.png` | developer instance: icon in light mode | the FORA mark, see below | Apache-2.0 |
| `fora-mark-white.png` | developer instance: icon in dark mode; publisher instance: icon in both modes | the FORA mark, see below | Apache-2.0 |
| `fora-wordmark.svg`, `fora-wordmark.png` | publisher instance: logo in both modes, including emails; developer dark-mode fallback; SVG is the outlined source, PNG is uploaded | website wordmark rendered from Instrument Sans, see below | SIL Open Font License 1.1 (`OFL.txt`) |
| `fora-wordmark-dark.png` | developer instance: light-mode logo, including emails | same outlined SVG, recolored `#12121a` | SIL Open Font License 1.1 (`OFL.txt`) |
| `instrument-sans.ttf` | both instances: font of the sign-in pages and emails | Instrument Sans, see below | SIL Open Font License 1.1 (`OFL.txt`) |
| `OFL.txt` | the font's licence, shipped with it | same commit as the font | |

Zitadel accepts each file up to 512 KB.

## The FORA mark

The mark is `website/public/favicon.svg` of
[FORA-Protocol/protocol](https://github.com/FORA-Protocol/protocol) at commit
`f04f5f660b2488f8b36791a7ed86100428701c20`. That SVG colours itself with a
`prefers-color-scheme` media query. Here each colour is fixed instead, because the
sign-in page chooses between a light and a dark logo itself.

The files are PNG rather than SVG because they also appear in emails, and mail
clients such as Gmail do not display SVG images.

To regenerate them, run from this directory with the SVG saved as `favicon.svg`.
The commands below reproduce both files byte for byte:

```sh
for c in black:000 white:fff; do
    n=${c%%:*}; f=${c#*:}
    sed -e 's|<style>[^<]*</style>||' -e "s|<path |<path fill=\"#$f\" |g" favicon.svg > "mark-$n.svg"
done
docker run --rm -v "$PWD":/w -w /w alpine:3.20 sh -c \
    'apk add --no-cache rsvg-convert >/dev/null &&
     for n in black white; do rsvg-convert -w 512 -h 512 -f png -o fora-mark-$n.png mark-$n.svg; done'
rm mark-black.svg mark-white.svg
```

## The wordmark

The [FORA website](https://fora-protocol.org/) header reads `fora protocol` in
Instrument Sans 600, with `-0.03em` letter spacing and `#e8e8f0` text. The outlined
SVG uses the bundled font at those settings, centered on a 720 × 160 transparent
canvas. The email template displays it at 180 × 40, so the wordmark stays sharp
on high-density screens without the old square logo's height. The browser icon
keeps the symbol.

Regenerate the email PNG from the SVG with librsvg:

```sh
rsvg-convert -w 720 -h 160 -f png -o fora-wordmark.png fora-wordmark.svg
sed 's/#e8e8f0/#12121a/g' fora-wordmark.svg | rsvg-convert -w 720 -h 160 -f png -o fora-wordmark-dark.png
```

## The font

`instrument-sans.ttf` is `ofl/instrumentsans/InstrumentSans[wdth,wght].ttf` of
[google/fonts](https://github.com/google/fonts) at commit
`2fc9491da70cf3e1bafd3c3da49244c5a496e9d5`, unmodified. It is the variable font, so
one file covers every weight the pages use. `OFL.txt` comes from the same directory
and commit; the licence names no Reserved Font Name.

The file is renamed because `curl -F file=@…` reads a comma in a file name as a
separator between two files.
