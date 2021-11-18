package globals

type Globals struct {
	Config string `kong:"flag,name='config',short='c',env='CONFIG',help='The configuration file to use',default='config.yaml'"`
}
