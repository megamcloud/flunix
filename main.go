// HTTP/2 web server with built-in support for Lua, Markdown, GCSS, Amber and JSX.
package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	internallog "log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xyproto/datablock"
	"github.com/xyproto/unzip"
	"github.com/yuin/gopher-lua"
)

const (
	versionString = "Algernon 1.4"
	description   = "HTTP/2 Web Server"
)

var (
	// Struct for reading from the filesystem, with the possibility of caching
	fs *datablock.FileStat
)

func main() {
	var err error

	// Initialize the server configuration structure
	ac := newAlgernonConfig()

	// Flags, version string output and profiling
	ac.init()
	defer os.RemoveAll(ac.serverTempDir)

	// Request handlers
	mux := http.NewServeMux()

	// Read mime data from the system, if available
	initializeMime()

	// Log to a file as JSON, if a log file has been specified
	if ac.serverLogFile != "" {
		f, errJSONLog := os.OpenFile(ac.serverLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, ac.defaultPermissions)
		if errJSONLog != nil {
			log.Warn("Could not log to", ac.serverLogFile, ":", errJSONLog.Error())
			// Try another filename
			ac.serverLogFile = strings.Replace(ac.serverLogFile, ".log", "2.log", 1)
			f, errJSONLog = os.OpenFile(ac.serverLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, ac.defaultPermissions)
			if errJSONLog != nil {
				log.Warn("Could not log to", ac.serverLogFile, ":", errJSONLog.Error())
			}
			//
		}
		// If we have a log file, log there
		if errJSONLog == nil {
			log.SetFormatter(&log.JSONFormatter{})
			log.SetOutput(f)
			//} else {
			//ac.fatalExit(errJSONLog)
		}
	} else if ac.quietMode {
		// If quiet mode is enabled and no log file has been specified, disable logging
		log.SetOutput(ioutil.Discard)
	}

	if ac.quietMode {
		os.Stdout.Close()
		os.Stderr.Close()
	}

	// Create a new FileStat struct, with optional caching (for speed).
	// Clear the cache every N minutes.
	fs = datablock.NewFileStat(ac.cacheFileStat, ac.defaultStatCacheRefresh)

	// Check if the given directory really is a directory
	if !fs.IsDir(ac.serverDir) {
		// Possibly a file
		filename := ac.serverDir
		// Check if the file exists
		if fs.Exists(filename) {
			if ac.markdownMode {
				// Serve the given Markdown file as a static HTTP server
				ac.serveStaticFile(filename, ac.defaultWebColonPort)
				return
			}
			// Switch based on the lowercase filename extension
			switch strings.ToLower(filepath.Ext(filename)) {
			case ".md", ".markdown":
				// Serve the given Markdown file as a static HTTP server
				ac.serveStaticFile(filename, ac.defaultWebColonPort)
				return
			case ".zip", ".alg":
				// Assume this to be a compressed Algernon application
				err := unzip.Extract(filename, ac.serverTempDir)
				if err != nil {
					log.Fatalln(err)
				}
				// Use the directory where the file was extracted as the server directory
				ac.serverDir = ac.serverTempDir
				// If there is only one directory there, assume it's the
				// directory of the newly extracted ZIP file.
				if filenames := getFilenames(ac.serverDir); len(filenames) == 1 {
					fullPath := filepath.Join(ac.serverDir, filenames[0])
					if fs.IsDir(fullPath) {
						// Use this as the server directory instead
						ac.serverDir = fullPath
					}
				}
				// If there are server configuration files in the extracted
				// directory, register them.
				for _, filename := range ac.serverConfigurationFilenames {
					configFilename := filepath.Join(ac.serverDir, filename)
					if fs.Exists(configFilename) {
						ac.serverConfigurationFilenames = append(ac.serverConfigurationFilenames, configFilename)
					}
				}
				// Disregard all configuration files from the current directory
				// (filenames without a path separator), since we are serving a
				// ZIP file.
				for i, filename := range ac.serverConfigurationFilenames {
					if strings.Count(filepath.ToSlash(filename), "/") == 0 {
						// Remove the filename from the slice
						ac.serverConfigurationFilenames = append(ac.serverConfigurationFilenames[:i], ac.serverConfigurationFilenames[i+1:]...)
					}
				}
			default:
				ac.singleFileMode = true
			}
		} else {
			ac.fatalExit(errors.New("File does not exist: " + filename))
		}
	}

	// Make a few changes to the defaults if we are serving a single file
	if ac.singleFileMode {
		ac.debugMode = true
		ac.serveJustHTTP = true
	}

	// Console output
	if !ac.quietMode && !ac.singleFileMode && !ac.simpleMode {
		fmt.Println(banner())
	}

	// Dividing line between the banner and output from any of the configuration scripts
	if len(ac.serverConfigurationFilenames) > 0 && !ac.quietMode {
		fmt.Println("--------------------------------------- - - · ·")
	}

	// Disable the database backend if the BoltDB filename is /dev/null
	if ac.boltFilename == "/dev/null" {
		ac.useNoDatabase = true
	}

	if !ac.useNoDatabase {
		// Connect to a database and retrieve a Permissions struct
		ac.perm, err = ac.aquirePermissions()
		if err != nil {
			log.Fatalln("Could not find a usable database backend.")
		}
	}

	// Lua LState pool
	ac.luapool = &lStatePool{saved: make([]*lua.LState, 0, 4)}
	atShutdown(func() {
		ac.luapool.Shutdown()
	})

	// TODO: save repl history + close luapool + close logs ++ at shutdown

	if ac.singleFileMode && filepath.Ext(ac.serverDir) == ".lua" {
		ac.luaServerFilename = ac.serverDir
		if ac.luaServerFilename == "index.lua" || ac.luaServerFilename == "data.lua" {
			log.Warn("Using " + ac.luaServerFilename + " as a standalone server!\nYou might wish to serve a directory instead.")
		}
		ac.serverDir = filepath.Dir(ac.serverDir)
		ac.singleFileMode = false
	}

	// Read server configuration script, if present.
	// The scripts may change global variables.
	var ranConfigurationFilenames []string
	for _, filename := range ac.serverConfigurationFilenames {
		if fs.Exists(filename) {
			if ac.verboseMode {
				fmt.Println("Running configuration file: " + filename)
			}
			if errConf := ac.runConfiguration(filename, mux, false); errConf != nil {
				if ac.perm != nil {
					log.Error("Could not use configuration script: " + filename)
					ac.fatalExit(errConf)
				} else {
					log.Warn("Not running " + filename + " because the database backend is disabled.")
				}
			}
			ranConfigurationFilenames = append(ranConfigurationFilenames, filename)
		}
	}
	// Only keep the active ones. Used when outputting server information.
	ac.serverConfigurationFilenames = ranConfigurationFilenames

	// Run the standalone Lua server, if specified
	if ac.luaServerFilename != "" {
		// Run the Lua server file and set up handlers
		if ac.verboseMode {
			fmt.Println("Running Lua Server File")
		}
		if errLua := ac.runConfiguration(ac.luaServerFilename, mux, true); errLua != nil {
			log.Error("Error in Lua server script: " + ac.luaServerFilename)
			ac.fatalExit(errLua)
		}
	} else {
		// Register HTTP handler functions
		ac.registerHandlers(mux, "/", ac.serverDir, ac.serverAddDomain)
	}

	// Set the values that has not been set by flags nor scripts
	// (and can be set by both)
	ranServerReadyFunction := ac.finalConfiguration(ac.serverHost)

	// If no configuration files were being ran successfully,
	// output basic server information.
	if len(ac.serverConfigurationFilenames) == 0 {
		if !ac.quietMode {
			fmt.Println(ac.Info())
		}
		ranServerReadyFunction = true
	}

	// Dividing line between the banner and output from any of the
	// configuration scripts. Marks the end of the configuration output.
	if ranServerReadyFunction && !ac.quietMode {
		fmt.Println("--------------------------------------- - - · ·")
	}

	// Direct internal logging elsewhere
	internalLogFile, err := os.Open(ac.internalLogFilename)
	if err != nil {
		// Could not open the internalLogFilename filename, try using another filename
		internalLogFile, err = os.OpenFile("internal.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, ac.defaultPermissions)
		atShutdown(func() {
			// TODO This one is is special and should be closed after the other shutdown functions.
			//      Set up a "done" channel instead of sleeping.
			time.Sleep(100 * time.Millisecond)
			internalLogFile.Close()
		})
		if err != nil {
			ac.fatalExit(fmt.Errorf("Could not write to %s nor %s.", ac.internalLogFilename, "internal.log"))
		}
	}
	defer internalLogFile.Close()
	internallog.SetOutput(internalLogFile)

	// Serve filesystem events in the background.
	// Used for reloading pages when the sources change.
	// Can also be used when serving a single file.
	if ac.autoRefreshMode {
		ac.refreshDuration, err = time.ParseDuration(ac.eventRefresh)
		if err != nil {
			log.Warn(fmt.Sprintf("%s is an invalid duration. Using %s instead.", ac.eventRefresh, ac.defaultEventRefresh))
			// Ignore the error, since defaultEventRefresh is a constant and must be parseable
			ac.refreshDuration, _ = time.ParseDuration(ac.defaultEventRefresh)
		}
		if ac.autoRefreshDir != "" {
			// Only watch the autoRefreshDir, recursively
			ac.EventServer(ac.autoRefreshDir, "*")
		} else {
			// Watch everything in the server directory, recursively
			ac.EventServer(ac.serverDir, "*")
		}
	}

	// For communicating to and from the REPL
	ready := make(chan bool) // for when the server is up and running
	done := make(chan bool)  // for when the user wish to quit the server

	// The Lua REPL
	if !ac.serverMode {
		// If the REPL uses readline, the SIGWINCH signal is handled there
		go ac.REPL(ready, done)
	} else {
		// Ignore SIGWINCH if we are not going to use a REPL
		ignoreTerminalResizeSignal()
	}

	// Run the shutdown functions if graceful does not
	defer ac.generateShutdownFunction(nil)()

	// Serve HTTP, HTTP/2 and/or HTTPS
	if err := ac.serve(mux, done, ready); err != nil {
		ac.fatalExit(err)
	}
}
