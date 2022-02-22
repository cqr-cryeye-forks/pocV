package check

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/WAY29/pocV/internal/common/errors"
	"github.com/WAY29/pocV/pkg/xray/cel"
	"github.com/WAY29/pocV/pkg/xray/requests"
	xray_structs "github.com/WAY29/pocV/pkg/xray/structs"
	"github.com/WAY29/pocV/utils"
	"gopkg.in/yaml.v2"
)

type RequestFuncType func(ruleName string, rule xray_structs.Rule) error

func executeXrayPoc(oReq *http.Request, target string, poc *xray_structs.Poc) (isVul bool, err error) {
	isVul = false

	defer func() {
		if r := recover(); r != nil {
			err = errors.Wrapf(r.(error), "Run Xray Poc[%s] error", poc.Name)
			isVul = false
		}
	}()

	var (
		milliseconds int64
		tcpudpType   string = ""

		request       *http.Request
		response      *http.Response
		protoRequest  *xray_structs.Request
		protoResponse *xray_structs.Response

		oReqUrlString string

		requestFunc RequestFuncType
	)

	// 初始赋值
	if oReq != nil {
		oReqUrlString = oReq.URL.String()
	}

	utils.DebugF("Run Xray Poc[%s] for %s", poc.Name, target)

	c := cel.NewEnvOption()
	env, err := cel.NewEnv(&c)
	if err != nil {
		wrappedErr := errors.Wrap(err, "Environment creation error")
		utils.ErrorP(wrappedErr)
		return false, err
	}

	// 请求中的全局变量
	variableMap := make(map[string]interface{})

	// 定义渲染函数
	render := func(v string) string {
		for k1, v1 := range variableMap {
			_, isMap := v1.(map[string]string)
			if isMap {
				continue
			}
			v1Value := fmt.Sprintf("%v", v1)
			t := "{{" + k1 + "}}"
			if !strings.Contains(v, t) {
				continue
			}
			v = strings.ReplaceAll(v, t, v1Value)
		}
		return v
	}
	// 定义evaluateUpdateVariableMap
	evaluateUpdateVariableMap := func(env *cel.Env, set yaml.MapSlice) {
		for _, item := range set {
			k, expression := item.Key.(string), item.Value.(string)
			if expression == "newReverse()" {
				reverse := xrayNewReverse()
				variableMap[k] = reverse
				continue
			}
			env, err = cel.NewEnv(&c)
			if err != nil {
				wrappedErr := errors.Wrap(err, "Environment re-creation error")
				utils.ErrorP(wrappedErr)
				return
			}

			out, err := cel.Evaluate(env, expression, variableMap)
			if err != nil {
				wrappedErr := errors.Wrapf(err, "Evalaute expression error: %s", expression)
				utils.ErrorP(wrappedErr)
				continue
			}
			switch value := out.Value().(type) {
			case *xray_structs.UrlType:
				variableMap[k] = cel.UrlTypeToString(value)
			case int64:
				variableMap[k] = int(value)
			default:
				variableMap[k] = fmt.Sprintf("%v", out)
			}
		}
	}

	// 处理set
	c.UpdateCompileOptions(poc.Set)
	evaluateUpdateVariableMap(env, poc.Set)

	// 处理payload
	for _, setMapVal := range poc.Payloads.Payloads {
		setMap := setMapVal.Value.(yaml.MapSlice)
		c.UpdateCompileOptions(setMap)
		evaluateUpdateVariableMap(env, setMap)
	}
	// 渲染detail
	detail := &poc.Detail
	detail.Author = render(detail.Author)
	for k, v := range poc.Detail.Links {
		detail.Links[k] = render(v)
	}
	fingerPrint := &detail.FingerPrint
	for _, info := range fingerPrint.Infos {
		info.ID = render(info.ID)
		info.Name = render(info.Name)
		info.Version = render(info.Version)
		info.Type = render(info.Type)
	}
	fingerPrint.HostInfo.Hostname = render(fingerPrint.HostInfo.Hostname)
	vulnerability := &detail.Vulnerability
	vulnerability.ID = render(vulnerability.ID)
	vulnerability.Match = render(vulnerability.Match)

	// transport=http: request处理
	HttpRequestInvoke := func(ruleName string, rule xray_structs.Rule) error {
		var (
			ok      bool
			err     error
			ruleReq xray_structs.RuleRequest = rule.Request
		)

		// 渲染请求头，请求路径和请求体
		for k, v := range ruleReq.Headers {
			ruleReq.Headers[k] = render(v)
		}
		ruleReq.Path = render(strings.TrimSpace(ruleReq.Path))
		ruleReq.Body = render(strings.TrimSpace(ruleReq.Body))

		// 尝试获取缓存
		if request, protoRequest, protoResponse, ok = requests.XrayGetHttpRequestCache(&ruleReq); !ok || !rule.Request.Cache {
			// 获取protoRequest
			protoRequest, err = requests.ParseHttpRequest(oReq)
			if err != nil {
				wrappedErr := errors.Wrapf(err, "Run poc[%v] parse request error", poc.Name)
				return wrappedErr
			}

			// 处理Path
			if strings.HasPrefix(ruleReq.Path, "/") {
				protoRequest.Url.Path = path.Join(oReq.URL.Path, ruleReq.Path)
			} else if strings.HasPrefix(ruleReq.Path, "^") {
				protoRequest.Url.Path = ruleReq.Path[1:]
			}

			// 某些poc没有区分path和query，需要处理
			protoRequest.Url.Path = strings.ReplaceAll(protoRequest.Url.Path, " ", "%20")
			protoRequest.Url.Path = strings.ReplaceAll(protoRequest.Url.Path, "+", "%20")

			// 克隆请求对象
			request, _ = http.NewRequest(ruleReq.Method, fmt.Sprintf("%s://%s%s", protoRequest.Url.Scheme, protoRequest.Url.Host, protoRequest.Url.Path), strings.NewReader(ruleReq.Body))

			request.Header = oReq.Header.Clone()
			rawHeader := ""
			for k, v := range ruleReq.Headers {
				request.Header.Set(k, v)
				rawHeader += fmt.Sprintf("%s=%s\n", k, v)
			}
			protoRequest.RawHeader = []byte(strings.Trim(rawHeader, "\n"))

			// 发起请求
			response, milliseconds, err = requests.DoRequest(request, ruleReq.FollowRedirects)
			if err != nil {
				return err
			}

			// 获取protoResponse
			protoResponse, err = requests.ParseHttpResponse(response, milliseconds)
			if err != nil {
				wrappedErr := errors.Wrapf(err, "Run poc[%s] parse response error", poc.Name)
				return wrappedErr
			}

			// 设置缓存
			requests.XraySetHttpRequestCache(&ruleReq, request, protoRequest, protoResponse)
		} else {
			utils.DebugF("Hit http request cache[%s%s]", oReqUrlString, ruleReq.Path)
		}

		return nil
	}

	// transport=tcp/udp: request处理
	TCPUDPRequestInvoke := func(ruleName string, rule xray_structs.Rule) error {
		var (
			tcpudpTypeUpper = strings.ToUpper(tcpudpType)
			buffer          = make([]byte, 1024)

			content             = rule.Request.Content
			connectionID string = rule.Request.ConnectionID
			conn         net.Conn
			connCache    *net.Conn
			responseRaw  []byte
			readTimeout  int

			ok  bool
			err error
		)

		// 获取response缓存
		if responseRaw, protoResponse, ok = requests.XrayGetTcpUdpResponseCache(rule.Request.Content); !ok || !rule.Request.Cache {
			responseRaw = make([]byte, 0, 8192)
			// 获取connectionID缓存
			if connCache, ok = requests.XrayGetTcpUdpConnectionCache(connectionID); !ok {
				// 处理timeout
				readTimeout, err = strconv.Atoi(rule.Request.ReadTimeout)
				if err != nil {
					wrappedErr := errors.Wrapf(err, "Parse read_timeout[%s] to int  error", rule.Request.ReadTimeout)
					return wrappedErr
				}

				// 发起连接
				conn, err = net.Dial(tcpudpType, target)
				if err != nil {
					wrappedErr := errors.Wrapf(err, "%s connect to target[%s] error", tcpudpTypeUpper, target)
					return wrappedErr
				}

				// 设置读取超时
				err := conn.SetReadDeadline(time.Now().Add(time.Duration(readTimeout) * time.Second))
				if err != nil {
					wrappedErr := errors.Wrapf(err, "Set read_timeout[%d] error", tcpudpTypeUpper, readTimeout)
					return wrappedErr
				}

				// 设置连接缓存
				requests.XraySetTcpUdpConnectionCache(connectionID, &conn)
			} else {
				conn = *connCache
				utils.DebugF("Hit connection_id cache[%s]", connectionID)
			}

			// 获取protoRequest
			protoRequest, _ = requests.ParseTCPUDPRequest([]byte(content))

			// 发送数据
			_, err = conn.Write([]byte(content))
			if err != nil {
				wrappedErr := errors.Wrapf(err, "%s[%s] write error", tcpudpTypeUpper, connectionID)
				return wrappedErr
			}

			// 接收数据
			for {
				n, err := conn.Read(buffer)
				if err != nil {
					if err == io.EOF {
					} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					} else {
						wrappedErr := errors.Wrapf(err, "%s[%s] read error", tcpudpTypeUpper, connectionID)
						return wrappedErr
					}
					break
				}
				responseRaw = append(responseRaw, buffer[:n]...)
			}

			// 获取protoResponse
			protoResponse, _ = requests.ParseTCPUDPResponse(responseRaw, &conn, tcpudpType)

			// 设置响应缓存
			requests.XraySetTcpUdpResponseCache(content, responseRaw, protoResponse)

		} else {
			utils.DebugF("Hit tcp/udp request cache[%s]", responseRaw)
		}

		return nil
	}

	// reqeusts总处理
	RequestInvoke := func(requestFunc RequestFuncType, ruleName string, rule xray_structs.Rule) (bool, error) {
		var (
			flag bool
			ok   bool
			err  error
		)
		err = requestFunc(ruleName, rule)
		if err != nil {
			return false, err
		}

		variableMap["request"] = protoRequest
		variableMap["response"] = protoResponse

		utils.DebugF("raw requests: \n%#s", string(protoRequest.Raw))
		utils.DebugF("raw response: \n%#s", string(protoResponse.Raw))

		// 执行表达式
		// ? 需要重新生成一遍环境，否则之前增加的变量定义不生效
		env, err = cel.NewEnv(&c)
		if err != nil {
			wrappedErr := errors.Wrap(err, "Environment re-creation error")
			return false, wrappedErr
		}
		out, err := cel.Evaluate(env, rule.Expression, variableMap)

		if err != nil {
			wrappedErr := errors.Wrapf(err, "Evalute rule[%s] expression error: %s", ruleName, rule.Expression)
			return false, wrappedErr
		}

		// 判断表达式结果
		flag, ok = out.Value().(bool)
		if !ok {
			flag = false
		}

		// 处理output
		c.UpdateCompileOptions(rule.Output)
		evaluateUpdateVariableMap(env, rule.Output)
		// 注入名为ruleName的函数
		c.NewResultFunction(ruleName, flag)

		return flag, nil
	}

	// 判断transport类型，设置requestInvoke
	if poc.Transport == "tcp" {
		tcpudpType = "tcp"
		requestFunc = TCPUDPRequestInvoke
	} else if poc.Transport == "udp" {
		tcpudpType = "udp"
		requestFunc = TCPUDPRequestInvoke
	} else {
		requestFunc = HttpRequestInvoke
	}

	// 执行rule
	ruleSlice := poc.Rules
	for _, ruleItem := range ruleSlice {
		_, err = RequestInvoke(requestFunc, ruleItem.Key, ruleItem.Value)
		if err != nil {
			return false, err
		}
	}

	// 判断poc总体表达式结果
	// ? 需要重新生成一遍环境，否则之前增加的结果函数不生效
	env, err = cel.NewEnv(&c)
	if err != nil {
		wrappedErr := errors.Wrap(err, "Environment re-creation error")
		utils.ErrorP(wrappedErr)
		return false, err
	}

	successVal, err := cel.Evaluate(env, poc.Expression, variableMap)
	if err != nil {
		wrappedErr := errors.Wrapf(err, "Evalute poc[%s] expression error: %s", poc.Name, poc.Expression)
		return false, wrappedErr
	}

	isVul, ok := successVal.Value().(bool)
	if !ok {
		isVul = false
	}

	return isVul, nil
}