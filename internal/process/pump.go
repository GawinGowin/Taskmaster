package process

import (
	"bufio"
	"errors"
	"os"
)

// maxLine は 1 行の上限。docker の json-file と同じ 16 KiB。
const maxLine = 16 * 1024

// pump はパイプの読み端を読み、そのまま sink に流す。
// 孫が書き端を握ると EOF が来ないので、終了は誰も待たない。
func pump(r *os.File, file *Sink) {
	defer r.Close()

	br := bufio.NewReaderSize(r, maxLine)
	for {
		// 16 KiB を超える行は ErrBufferFull で切れて返る。
		// bufio.Scanner は ErrTooLong で読むのをやめてしまう。
		line, err := br.ReadSlice('\n')

		// EOF でも改行の無い最後の断片が返るので、err より先に書く。
		// 構造化出力を壊さないよう、プレフィックスは付けない。
		if len(line) > 0 {
			file.Write(line)
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}
