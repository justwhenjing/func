package functions

import (
	"archive/zip"
	"bytes"

	"knative.dev/func/generate"
	"knative.dev/func/pkg/filesystem"
)

// 主要作用: 将templates目录下的文件打包成一个zip文件，并将其存储在generate.TemplatesZip变量中。
// 然后通过newEmbeddedTemplatesFS函数将这个zip文件加载到内存中，并返回一个filesystem.Filesystem接口的实现。
// 这个实现可以被用于访问templates目录下的文件。

//go:generate go run ../../generate/templates/main.go

func newEmbeddedTemplatesFS() filesystem.Filesystem {
	archive, err := zip.NewReader(bytes.NewReader(generate.TemplatesZip), int64(len(generate.TemplatesZip)))
	if err != nil {
		panic(err)
	}
	return filesystem.NewZipFS(archive)
}

var EmbeddedTemplatesFS filesystem.Filesystem = newEmbeddedTemplatesFS()
