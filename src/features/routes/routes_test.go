package routes

import (
	"path/filepath"
	"testing"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

func newTestStorage(t *testing.T) *storage.GraphStorage {
	t.Helper()
	return storage.NewGraphStorage(filepath.Join(t.TempDir(), "knowledge.lattice"))
}

func TestExtractGoRoutes(t *testing.T) {
	goCode := `package api

import "github.com/gin-gonic/gin"

func SetupRoutes(r *gin.Engine) {
	r.GET("/api/v1/users", GetUsers)
	r.POST("/api/v1/users", CreateUser)
	r.DELETE("/api/v1/users/:id", DeleteUser)
}

func GetUsers(c *gin.Context) {}
func CreateUser(c *gin.Context) {}
func DeleteUser(c *gin.Context) {}
`
	routes := ExtractRoutesFromFile("src/api/routes.go", goCode)
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
	if routes[0].Method != "GET" || routes[0].Path != "/api/v1/users" || routes[0].HandlerName != "GetUsers" {
		t.Errorf("route 0 mismatch: %+v", routes[0])
	}
	if routes[1].Method != "POST" || routes[1].HandlerName != "CreateUser" {
		t.Errorf("route 1 mismatch: %+v", routes[1])
	}
}

func TestExtractPythonRoutes(t *testing.T) {
	pyCode := `from fastapi import FastAPI

app = FastAPI()

@app.get("/items/{item_id}")
def read_item(item_id: int):
    return {"item_id": item_id}

@app.post("/items")
def create_item(data: dict):
    return data
`
	routes := ExtractRoutesFromFile("app/main.py", pyCode)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].Method != "GET" || routes[0].Path != "/items/{item_id}" || routes[0].HandlerName != "read_item" {
		t.Errorf("route 0 mismatch: %+v", routes[0])
	}
}

func TestExtractTypeScriptRoutes(t *testing.T) {
	tsCode := `import express from 'express';
const router = express.Router();
// router.get('/commented-out', fakeHandler);
/*
router.post('/block-commented', fakeHandler);
*/
router.get('/products', listProducts);
router.post('/products', createProduct);
`
	routes := ExtractRoutesFromFile("src/routes/product.ts", tsCode)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes (comments ignored), got %d: %+v", len(routes), routes)
	}
	if routes[0].Method != "GET" || routes[0].Path != "/products" || routes[0].HandlerName != "listProducts" || routes[0].Line != 7 {
		t.Errorf("route 0 mismatch: %+v", routes[0])
	}
}

func TestExtractNestJSAndSpringRoutes(t *testing.T) {
	nestCode := `@Controller('api/users')
export class UsersController {
  @Get(':id')
  async findOne(id: string) {}
}`
	nestRoutes := ExtractRoutesFromFile("src/users.controller.ts", nestCode)
	if len(nestRoutes) != 1 || nestRoutes[0].Path != "/api/users/:id" || nestRoutes[0].HandlerName != "findOne" {
		t.Fatalf("unexpected NestJS routes: %+v", nestRoutes)
	}

	springCode := `@RestController
@RequestMapping("/api/v1")
public class OrderController {
    @GetMapping("/orders")
    public List<Order> listOrders() { return null; }
}`
	springRoutes := ExtractRoutesFromFile("src/OrderController.java", springCode)
	if len(springRoutes) != 1 || springRoutes[0].Path != "/api/v1/orders" || springRoutes[0].HandlerName != "listOrders" {
		t.Fatalf("unexpected Spring routes: %+v", springRoutes)
	}
}

func TestRouteManagerIndexAndFind(t *testing.T) {
	db := newTestStorage(t)

	// Handler symbol
	db.CreateNode("symbol:src/api/routes.go:GetUsers", []string{"L3Symbol"}, map[string]any{
		"id":        "symbol:src/api/routes.go:GetUsers",
		"name":      "GetUsers",
		"file_path": "src/api/routes.go",
		"level":     string(models.LevelL3),
	})

	rm := NewRouteManager(db)
	rm.IndexRoutes([]RouteEndpoint{
		{
			ID:          "route:GET:/api/v1/users",
			Method:      "GET",
			Path:        "/api/v1/users",
			HandlerName: "GetUsers",
			FilePath:    "src/api/routes.go",
			Line:        5,
			Framework:   "gin",
		},
		{
			ID:          "route:POST:/api/v1/payments",
			Method:      "POST",
			Path:        "/api/v1/payments",
			HandlerName: "ProcessPayment",
			FilePath:    "src/api/routes.go",
			Line:        10,
			Framework:   "gin",
		},
	})

	// 1. Find by path pattern
	found := rm.FindRoutes("users", "")
	if len(found) != 1 {
		t.Fatalf("expected 1 route for 'users', got %d", len(found))
	}
	if found[0].Path != "/api/v1/users" || found[0].HandlerID != "symbol:src/api/routes.go:GetUsers" {
		t.Errorf("route mismatch: %+v", found[0])
	}

	// 2. Verify edge EXPOSES_ROUTE created
	edges := db.GetOutgoingEdges("symbol:src/api/routes.go:GetUsers", "EXPOSES_ROUTE")
	if len(edges) != 1 || edges[0].TargetID != "route:GET:/api/v1/users" {
		t.Errorf("expected EXPOSES_ROUTE edge, got %+v", edges)
	}
}

