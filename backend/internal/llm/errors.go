package llm

import "errors"

// errStreamDone 流式读取终止信号（取消或 [DONE]）
var errStreamDone = errors.New("[llm] 流式读取结束")
