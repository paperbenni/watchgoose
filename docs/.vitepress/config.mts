import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'watchgoose',
  description: 'A dead-man\'s switch for a Linux work VM',
  base: '/watchgoose/',
  themeConfig: {
    nav: [
      { text: 'Guide', link: '/guide/how-it-works' },
      { text: 'Decisions', link: '/decisions' },
      { text: 'GitHub', link: 'https://github.com/paperbenni/watchgoose' }
    ],
    sidebar: [
      {
        text: 'Guide',
        items: [
          { text: 'How it works', link: '/guide/how-it-works' },
          { text: 'Network and ports', link: '/guide/network' },
          { text: 'Install', link: '/guide/install' },
          { text: 'Operate', link: '/guide/operate' },
          { text: 'Limits', link: '/guide/limits' }
        ]
      },
      {
        text: 'Background',
        items: [
          { text: 'Why it exists', link: '/origin' },
          { text: 'Terminology', link: '/CONTEXT' },
          { text: 'Design decisions', link: '/decisions' }
        ]
      }
    ],
    search: { provider: 'local' },
    socialLinks: [
      { icon: 'github', link: 'https://github.com/paperbenni/watchgoose' }
    ],
    editLink: {
      pattern: 'https://github.com/paperbenni/watchgoose/edit/main/docs/:path',
      text: 'Edit this page on GitHub'
    }
  }
})
