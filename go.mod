module github.com/free5gc/chf

go 1.14

require (
	github.com/antonfisher/nested-logrus-formatter v1.3.1
	github.com/asaskevich/govalidator v0.0.0-20210307081110-f21760c49a8d
	github.com/fclairamb/ftpserver v0.12.1
	github.com/fclairamb/ftpserverlib v0.20.0
	github.com/fclairamb/go-log v0.4.1
	github.com/free5gc/CDRUtil v0.0.0-00010101000000-000000000000
	github.com/free5gc/TarrifUtil v0.0.0-00010101000000-000000000000
	github.com/free5gc/openapi v1.0.4
	github.com/free5gc/util v1.0.3
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.7.3
	github.com/google/uuid v1.3.0
	github.com/sirupsen/logrus v1.9.0
	github.com/urfave/cli v1.22.5
	go.mongodb.org/mongo-driver v1.8.4
	gopkg.in/yaml.v2 v2.4.0
)

replace github.com/free5gc/openapi => /home/free5gc/openapi

replace github.com/free5gc/TarrifUtil => /home/free5gc/TarrifUtil

replace github.com/free5gc/CDRUtil => /home/free5gc/CDRUtil
