# Gritz UI v2

Modern React + TypeScript + Vite + shadcn/ui frontend for Gritz.

## Development

```bash
pnpm run dev
```

Open [http://localhost:5173](http://localhost:5173) to view the app.

## Stack

- **React 19** - UI framework
- **TypeScript** - Type safety
- **Vite** - Build tool and dev server
- **TanStack Router** - File-based routing
- **TanStack Query** - Server state management
- **Tailwind CSS v4** - Styling
- **shadcn/ui** - Component library

## Project Structure

```
webui/
├── src/
│   ├── routes/        # File-based routes (TanStack Router)
│   │   ├── __root.tsx # Root layout
│   │   └── index.tsx  # Home page (/)
│   ├── components/    # React components
│   │   └── ui/        # shadcn/ui components
│   ├── lib/           # Utilities
│   └── main.tsx       # Entry point
├── public/            # Static assets
└── package.json       # Dependencies
```

## Routing

Routes are file-based in `src/routes/`. The TanStack Router plugin automatically generates the route tree.

- `src/routes/index.tsx` → `/`
- `src/routes/tasks.tsx` → `/tasks`
- `src/routes/tasks/$id.tsx` → `/tasks/:id`

## Adding Components

Use the shadcn CLI to add components:

```bash
npx shadcn@latest add button
npx shadcn@latest add card
```

## Build

```bash
pnpm run build
pnpm run preview
```
