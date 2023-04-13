module github.com/free5gc/chf

go 1.14

require (
	cloud.google.com/go/compute/metadata v0.2.3 // indirect
	github.com/antonfisher/nested-logrus-formatter v1.3.1
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2
	github.com/fclairamb/ftpserver v0.13.0
	github.com/fclairamb/ftpserverlib v0.21.0
	github.com/fclairamb/go-log v0.4.1
	github.com/fiorix/go-diameter v3.0.2+incompatible
	github.com/free5gc/CDRUtil v0.0.0-00010101000000-000000000000
	github.com/free5gc/RatingUtil v0.0.0-00010101000000-000000000000
	github.com/free5gc/openapi v1.0.6
	github.com/free5gc/util v1.0.3
	github.com/gin-contrib/cors v1.4.0
	github.com/gin-gonic/gin v1.9.0
	github.com/google/uuid v1.3.0
	github.com/ishidawataru/sctp v0.0.0-20230406120618-7ff4192f6ff2 // indirect
	github.com/jlaffaye/ftp v0.1.0
	github.com/juju/fslock v0.0.0-20160525022230-4d5c94c67b4b
	github.com/sirupsen/logrus v1.9.0
	github.com/stretchr/testify v1.8.2
	github.com/urfave/cli v1.22.12
	go.mongodb.org/mongo-driver v1.11.3
	gopkg.in/yaml.v2 v2.4.0
)

replace github.com/free5gc/openapi => /home/free5gc/openapi

replace github.com/free5gc/RatingUtil => /home/free5gc/RatingUtil

replace github.com/free5gc/CDRUtil => /home/free5gc/CDRUtil
