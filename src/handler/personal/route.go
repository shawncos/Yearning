package personal

import "github.com/cookieY/yee"

func PersonalRestFulAPis() yee.RestfulAPI {
	return yee.RestfulAPI{
		Post: Post,
		Get:  GET,
	}
}
