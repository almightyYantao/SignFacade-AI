# AI 广告图生成网站

前端使用 React + Antd，后端使用 Go，调用 Nano Banana Pro 接口生成门头广告效果图。

## 目录结构
- `frontend` 前端项目（Vite + React + Antd）
- `backend` Go 服务端（转发 API、隐藏 api_key）
- `docker-compose.yml` 一键部署前后端

## 本地开发运行

1. 启动后端

```bash
cd backend
go run .
```

2. 启动前端

```bash
cd frontend
npm install
npm run dev
```

前端默认通过 Vite 代理访问 `http://localhost:8080/api`。

## Docker 部署

1. 在 `backend/.env` 中配置好后端环境变量（AI_PROVIDER、KIE_API_KEY/APIMART_API_KEY 等）。
2. 在项目根目录执行：

```bash
docker compose up -d --build
```

3. 访问：
- 前端：http://localhost
- 后端健康检查：http://localhost/api/health

4. 常用命令：

```bash
# 查看日志
docker compose logs -f

# 重启
docker compose restart

# 停止并删除容器
docker compose down
```

说明：
- 前端容器使用 Nginx 托管静态文件，并将 `/api` 反向代理到后端容器。
- 后端 `data.db` 使用 Docker volume `backend-data` 持久化。

## 接口说明
- `POST /api/tasks` 创建任务
- `GET /api/tasks/{task_id}` 查询任务状态
- `POST /api/tasks/wait` 阻塞等待
- `GET /api/usage` 使用量统计

## 说明
- 前端支持上传门头照片、平面设计图、参考效果图，也支持填写图片 URL。
- 表单参数会拼接成提示词并发送给后端。
- 生成结果会轮询状态并展示图片、分辨率与 token 信息。
- `AI_PROVIDER` 支持：`legacy`、`kie`、`apimart`。
