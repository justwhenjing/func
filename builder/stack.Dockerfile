FROM zxnp-pict-release-docker.artsh.zte.com.cn/os/alpine:3.19.4

# 修改镜像源
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.zte.com.cn/g' /etc/apk/repositories

# 安装基础组件
RUN apk add --no-cache --no-scripts \
    go \
    gcc \
    musl-dev \
    git \
    make \
    bash

# 设置 Go 环境变量
ENV GOPATH=/go
ENV PATH=$PATH:/usr/lib/go/bin:$GOPATH/bin
ENV GOPROXY=

# 设置CNB属性
ENV CNB_USER_ID=1000
ENV CNB_GROUP_ID=1000
LABEL io.buildpacks.stack.id="zte.com.cn/stack"

RUN mkdir -p /cnb && echo '[stack]\nid = "zte.com.cn/stack"' > /cnb/stack.toml

RUN echo "==> Add group and user ..." \
    && umask 027 \
    && addgroup -g ${CNB_GROUP_ID} -S cnb \
    && adduser cnb -u ${CNB_USER_ID} -H -D -s /bin/sh -G cnb \
    && mkdir -p /home/cnb \
    && chmod 750 /home/cnb \
    && chown cnb:cnb /home/cnb
