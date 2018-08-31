// Copyright 2018 John Deng (hi.devops.io@gmail.com).
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"regexp"
	"github.com/kataras/iris"
	"github.com/kataras/iris/context"
	"github.com/kataras/iris/middleware/i18n"
	"github.com/kataras/iris/middleware/logger"
	"github.com/hidevopsio/hiboot/pkg/log"
	"github.com/hidevopsio/hiboot/pkg/system"
	"github.com/hidevopsio/hiboot/pkg/utils/reflector"
	"github.com/hidevopsio/hiboot/pkg/utils/io"
	"github.com/hidevopsio/hiboot/pkg/app"
	"errors"
	"runtime"
)

const (
	pathSep = "/"

	initMethodName = "Init"

	BeforeMethod = "Before"
	AfterMethod  = "After"
)

// Application is the struct of web Application
// TODO: application should be singleton and private
type application struct {
	app.BaseApplication
	webApp          *iris.Application
	jwtEnabled      bool
	httpMethods     []string
	anonControllers []interface{}
	jwtControllers  []interface{}
	dispatcher      dispatcher
	controllerMap   map[string][]interface{}
}

var (
	// controllers global controllers container
	webControllers []interface{}
	compiledRegExp = regexp.MustCompile(`\{(.*?)\}`)

	ControllersNotFoundError = errors.New("[app] controllers not found")
	InvalidControllerError = errors.New("[app] invalid controller")
)


func HideBanner() {
	app.HideBanner()
}

// Run run web application
func (a *application) Run() {
	serverPort := ":8080"
	conf := a.SystemConfig()
	if conf != nil && conf.Server.Port != "" {
		serverPort = fmt.Sprintf(":%v", conf.Server.Port)
	}
	// TODO: WithCharset should be configurable
	a.webApp.Run(iris.Addr(fmt.Sprintf(serverPort)), iris.WithConfiguration(DefaultConfiguration()))
}

func (a *application) add(controllers ...interface{}) {
	for _, controller := range controllers {

		ifcField := reflector.GetEmbeddedInterfaceField(controller)
		if ifcField.Anonymous {
			ctrlTypeName := ifcField.Name
			controllers := a.controllerMap[ctrlTypeName]
			a.controllerMap[ctrlTypeName] = append(controllers, controller)
		}
	}
}

// EnsureWorkDir ensure that current dir contains config/application.yml
func (a *application) EnsureWorkDir(skip int)  {
	// check if app is running on ide
	var path string
	if _, file, _, ok := runtime.Caller(2); ok && strings.Contains(os.Args[0], "go_build_") {
		path = io.BaseDir(file)
	} else {
		path = io.GetWorkDir()
	}
	lastPath := ""
	for {
		//log.Debugf("%v", path)
		configPath := filepath.Join(path, "config", "application.yml")
		if !io.IsPathNotExist(configPath) {
			io.ChangeWorkDir(path)
			break
		}

		path = io.BaseDir(path)
		if lastPath == path {
			break
		}
		lastPath = path
	}
}

// Init init web application
func (a *application) Init(controllers ...interface{}) error {

	// run base Init
	err := a.BaseApplication.Init(controllers...)
	if err != nil {
		return err
	}

	// before init
	a.BeforeInitialization()

	a.httpMethods = []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodConnect,
		http.MethodOptions,
		http.MethodTrace,
	}

	systemConfig := a.SystemConfig()
	if systemConfig != nil {
		log.SetLevel(systemConfig.Logging.Level)
		log.Infof("Starting Hiboot web application %v on localhost with PID %v (%v)", systemConfig.App.Name, os.Getpid(), a.WorkDir)
		log.Infof("The following profiles are active: %v, %v", systemConfig.App.Profiles.Active, systemConfig.App.Profiles.Include)
	}

	f := a.ConfigurableFactory()
	f.SetInstance("applicationContext", a)

	// build auto configurations
	a.BuildConfigurations()

	a.controllerMap = make(map[string][]interface{})
	if len(controllers) == 0 {
		a.add(webControllers...)
	} else {
		a.add(controllers...)
	}

	a.webApp = iris.New()

	//TODO: move out to starter/logging
	customLogger := logger.New(logger.Config{
		// Status displays status code
		Status: true,
		// IP displays request's remote address
		IP: true,
		// Method displays the http method
		Method: true,
		// Path displays the request path
		Path: true,
		// Query appends the url query to the Path.
		//Query: true,

		//Columns: true,

		// if !empty then its contents derives from `ctx.Values().Get("logger_message")
		// will be added to the logs.
		MessageContextKeys: []string{"logger_message"},

		// if !empty then its contents derives from `ctx.GetHeader("User-Agent")
		MessageHeaderKeys: []string{"User-Agent"},
	})

	// TODO: it should be configurable
	a.webApp.Use(customLogger)

	// The only one Required:
	// here is how you define how your own context will
	// be created and acquired from the iris' generic context pool.
	a.webApp.ContextPool.Attach(func() context.Context {
		return &Context{
			// Optional Part 3:
			Context: context.NewContext(a.webApp),
		}
	})

	err = a.initLocale()
	if err != nil {
		log.Debug(err)
	}

	// first register anon controllers
	err = a.RegisterController(new(AnonController))

	// call AfterInitialization with factory interface
	a.AfterInitialization()

	return nil
}

func (a *application) RegisterController(controller interface{}) error {
	// get from controller map
	// parse controller type
	controllerInterfaceName, err := reflector.GetName(controller)
	if err != nil {
		return InvalidControllerError
	}
	controllers, ok := a.controllerMap[controllerInterfaceName]
	if ok {
		return a.dispatcher.register(a.webApp, controllers)
	}
	return ControllersNotFoundError
}

func (a *application) Use(handlers ...context.Handler) {
	// pass user's instances
	for _, hdl := range handlers {
		a.webApp.Use(hdl)
	}
}


func (a *application) initLocale() error {
	// TODO: localePath should be configurable in application.yml
	// locale:
	//   en-US: ./config/i18n/en-US.ini
	//   cn-ZH: ./config/i18n/cn-ZH.ini
	// TODO: or
	// locale:
	//   path: ./config/i18n/
	localePath := "config/i18n/"
	if io.IsPathNotExist(localePath) {
		return &system.NotFoundError{Name: localePath}
	}

	// parse language files
	languages := make(map[string]string)
	err := filepath.Walk(localePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Fatal(err)
		}
		//*files = append(*files, path)
		lng := strings.Replace(path, localePath, "", 1)
		lng = io.BaseDir(lng)
		lng = io.Basename(lng)

		if lng != "" && path != localePath+lng {
			//languages[lng] = path
			if languages[lng] == "" {
				languages[lng] = path
			} else {
				languages[lng] = languages[lng] + ", " + path
			}
			//log.Debugf("%v, %v", lng, languages[lng])
		}
		return nil
	})
	if err != nil {
		return err
	}

	globalLocale := i18n.New(i18n.Config{
		Default:      "en-US",
		URLParameter: "lang",
		Languages:    languages,
	})

	a.webApp.Use(globalLocale)

	return nil
}

// Add add controller to controllers container
func RestController(controllers ...interface{}) {
	webControllers = append(webControllers, controllers...)
}

// NewApplication create new web application instance and init it
func NewApplication(controllers ...interface{}) app.Application {
	wa := new(application)
	wa.EnsureWorkDir(2)
	err := wa.Init(controllers...)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	return wa
}