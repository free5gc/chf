module github.com/free5gc/chf

go 1.14

require (
	github.com/antonfisher/nested-logrus-formatter v1.3.1
	github.com/asaskevich/govalidator v0.0.0-20210307081110-f21760c49a8d
	github.com/free5gc/CDRUtil v0.0.0-00010101000000-000000000000
	github.com/free5gc/TarrifUtil v0.0.0-00010101000000-000000000000
	github.com/free5gc/openapi v1.0.4
	github.com/free5gc/util v1.0.3
	github.com/fsnotify/fsnotify v1.5.4
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.7.3
	github.com/google/uuid v1.3.0
	github.com/sirupsen/logrus v1.8.1
	github.com/urfave/cli v1.22.5
	golang.org/x/sys v0.0.0-20220829200755-d48e67d00261 // indirect
	gopkg.in/yaml.v2 v2.4.0

)

replace github.com/free5gc/openapi => /home/uduck/openapi

replace github.com/free5gc/TarrifUtil => /home/uduck/TarrifUtil

replace github.com/free5gc/CDRUtil => /home/uduck/CDRUtil
