#!/usr/bin/env bash
# Authenticode-signs a Windows file in place. With SIGN_CERT and SIGN_KEY (PEM files)
# it uses them; otherwise a self-signed test certificate, made once in
# ~/.cache/hub-signing, which Windows does not trust: no supplier certificate exists
# yet (docs/DEPLOYMENT.md, "Signing").
#
#   windows/sign.sh file.exe
set -euo pipefail
FILE=$1
CERT=${SIGN_CERT:-} KEY=${SIGN_KEY:-}
if [ -z "$CERT" ] || [ -z "$KEY" ]; then
  D=$HOME/.cache/hub-signing
  CERT=$D/test-ca.crt KEY=$D/test-ca.key
  if [ ! -s "$CERT" ]; then
    mkdir -p "$D" && chmod 700 "$D"
    # A config file rather than -addext: macOS's LibreSSL may lack it.
    printf '[req]\ndistinguished_name=dn\nx509_extensions=ext\nprompt=no\n[dn]\nCN=DHIS2 Facility Hub TEST signing\nO=TEST ONLY\n[ext]\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=codeSigning\n' >"$D/req.cnf"
    openssl req -x509 -newkey rsa:3072 -sha256 -days 3650 -nodes -keyout "$KEY" -out "$CERT" -config "$D/req.cnf" 2>/dev/null
  fi
fi
osslsigncode sign -certs "$CERT" -key "$KEY" -h sha256 -n "DHIS2 Facility Hub" \
  -i https://github.com/dhis2-utils/poc-data-proxy -in "$FILE" -out "$FILE.signed" >/dev/null
mv "$FILE.signed" "$FILE"
