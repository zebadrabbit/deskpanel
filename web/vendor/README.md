# Vendored: ESP Web Tools

`esp-web-tools/` is `dist/web/` of [esp-web-tools](https://github.com/esphome/esp-web-tools) **10.5.2** from npm,
unchanged, plus the package's `LICENSE` (Apache 2.0). The flasher page loads it from here, so it makes no
requests to anyone else.

Checked against npm's integrity hash:
`sha512-i5qsHv1bnrb/9gmKEN7qIaDTMOMy6OctrgFVXStrU2s79erY2Z7IKydveNk0eDhQTarfwyR4eXspOUIF6kYawg==`

To update (on a machine with npm or Docker; `X.Y.Z` is the new version):

```sh
cd /tmp && npm pack esp-web-tools@X.Y.Z          # or: docker run --rm -v "$PWD":/w -w /w node:22-alpine npm pack esp-web-tools@X.Y.Z
npm view esp-web-tools@X.Y.Z dist.integrity      # must equal:
python3 -c "import hashlib,base64; print('sha512-' + base64.b64encode(hashlib.sha512(open('esp-web-tools-X.Y.Z.tgz','rb').read()).digest()).decode())"
tar xzf esp-web-tools-X.Y.Z.tgz
rm -rf <repo>/web/vendor/esp-web-tools && mkdir <repo>/web/vendor/esp-web-tools
cp package/dist/web/* package/LICENSE <repo>/web/vendor/esp-web-tools/
```

Then update the version and hash above, and test a flash from the page.
