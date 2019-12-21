#!/bin/sh
echo 'Try editing the markdown file in samples/welcome and see the'
echo 'results instantly in the browser at http://localhost:3000/'
echo '\n'
cd flunix
mv algernon flunix
./flunix --dev --conf serverconf.lua --dir samples/welcome --httponly --debug --autorefresh --bolt --server "$@"
