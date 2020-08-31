# aptblob

## Starting a Repository

```
mkdir bucket
BUCKET="file://$(pwd)/bucket"
gpg --gen-key
KEYID=42CAFE...

go run . init -k $KEYID "$BUCKET" stable <<EOF
Origin: stable
Label: stable
Codename: stable
Architectures: amd64
Components: main
Description: Apt repository for Foo
EOF

go run . upload -k $KEYID "$BUCKET" stable mypackage.deb
```
