package fetcher

// Synthetic pages for the text rules in pagetext.go. Each fixture keeps the
// words a rule reads (a not-found notice, a parking notice, a challenge
// phrase, a sign-in form) but uses reserved example hosts and made-up text.
// They stand in for the real pages the rules were first measured against.

const (
	// Not-found pages without a target-status warning, under titles no title
	// rule reads as one.

	// publishingNotFoundBody is a publishing platform's 404 page, behind its
	// navigation.
	publishingNotFoundBody = `[Home](https://publishing.example/)

[Open in app](https://publishing.example/app)

Sign up

[Sign in](https://publishing.example/signin)

[Search](https://publishing.example/search)

PAGE NOT FOUND

## 404

## Out of nothing, something.

You can find (just about) anything on Example Publishing — apparently even a page that doesn’t exist. Maybe these stories will take you somewhere new?

[Home](https://publishing.example/)

#### [A story](https://publishing.example/a-story)

[An Author](https://publishing.example/@author)

Sep 12, 2026

·

12 min read

Member-only`

	// weeklyNewsNotFoundBody is untitled.
	weeklyNewsNotFoundBody = `[![Image 2: Example Weekly](https://images.example/weekly.svg)Example Weekly](https://weekly-news.example/)

[Subscribe](https://weekly-news.example/subscribe)

×Search

Sign in

*   View Profile
*   Sign out

*   [](https://weekly-news.example/)
*   [The Explainer](https://weekly-news.example/the-explainer)
*    More
    *   [Politics](https://weekly-news.example/politics)
    *   [World News](https://weekly-news.example/tag/world-news)

*   [Newsletter sign up Newsletter](https://weekly-news.example/newsletters)

# Sorry!

The page you're looking for can't be found.

![Image 4: 404 page not found](https://images.example/404.png)

Please try searching our site or [start again on our homepage](https://weekly-news.example/).

Search

[](https://weekly-news.example/)
*   [About Us](https://weekly-news.example/about-us)
*   [Terms and Conditions](https://weekly-news.example/terms-conditions/)
*   [Privacy Policy](https://weekly-news.example/privacy-policy/)
*   [Cookie Policy](https://weekly-news.example/cookies-policy/)
*   [FAQ](https://weekly-news.example/faq)`

	// marketResearchNotFoundBody is titled "Example Research".
	marketResearchNotFoundBody = `![Image 1](https://images.example/arrow-left.png)

[Home](https://market-research.example/)

Research

[Starred](https://market-research.example/)[Report](https://market-research.example/)[Morning Lineup](https://market-research.example/)

Tools

[Charts](https://market-research.example/)[Charts Scanner](https://market-research.example/)[Earnings Tool](https://market-research.example/)[Seasonality Tool](https://market-research.example/)[Trend Analyzer](https://market-research.example/)

My Portfolios

[No results found. Try typing _home_ or _$MSFT_](https://market-research.example/)[Search](https://market-research.example/)[Research](https://market-research.example/)[Tools](https://market-research.example/)[Portfolios](https://market-research.example/)Log In

Account

[Account](https://market-research.example/account/)[Settings](https://market-research.example/account/)[Sign Out](https://market-research.example/logout-2/)

# 404

# Sorry, but we can't find the page you're looking for.

Go Home

©2026 Example Research`

	// developerForumNotFoundBody is titled "หน้าไม่พบ | Example Forum" (Thai for
	// "Page not found").
	developerForumNotFoundBody = `## 404 - หน้าไม่พบ

ขออภัย, หน้าที่คุณกำลังค้นหาไม่มีอยู่หรืออาจได้รับการย้าย. กรุณาตรวจสอบ URL และลองอีกครั้ง.

ในระหว่างนี้, ทำไมไม่ลองสำรวจสิ่งใหม่? เราเพิ่งเปิดตัวเกมใหม่ที่ชื่อ [**PG**](https://developer-forum.example/)Slot ซึ่งเป็นเกม Java สุดล้ำจาก **Example Forum**.

สนใจเกมคาสิโน? ลองเล่น **[PG Slot](https://developer-forum.example/)**, เกมที่ให้ความสนุกไม่รู้จบ!

pgslot: กลับไปที่หน้าแรกของเราได้ที่ [EXAMPLE FORUM](https://developer-forum.example/) เพื่อดูข้อมูลเพิ่มเติม.

It looks like the page you’re searching for doesn’t exist. But don’t worry, exciting things are happening at Example Forum! Did you know we're now diving into the world of gaming?

Our latest creation, PG Slot, is a groundbreaking Java game set to revolutionize the way you play.

Whether you’re here for PGSlot or curious about our latest innovations, stick around as we bring new experiences to life.

Head back to the homepage and explore our updates!`

	// networkNotFoundBody is titled "Top Content on Example Network": its
	// notice is its first long line.
	networkNotFoundBody = `[Skip to main content](https://professional-network.example/pulse/a-post/#main-content)[Example Network](https://professional-network.example/)

*   [Top Content](https://professional-network.example/top-content)
*   [People](https://professional-network.example/pub/dir)
*   [Learning](https://professional-network.example/learning)
*   [Jobs](https://professional-network.example/jobs)
*   [Games](https://professional-network.example/games)

[Join now](https://professional-network.example/signup)[Sign in](https://professional-network.example/login)[![Image 1](https://profesional-network.example/pulse/a-post/)](https://professional-network.example/login)

We can’t find the page you’re looking for.The page you’re looking for may have been moved, or may no longer exist. Try going back to the previous page or check out top Example Network content from expert professionals.

# What topics do you want to explore?`

	// businessNewsNotFoundBody is titled "Example Business News": a framework's 404
	// under the site's header, which the answer repeats.
	businessNewsNotFoundBody = `[Subscribe](https://business-news.example/newsletter)

Edition

English

[Business News](https://business-news.example/business-news)

[A.I.](https://business-news.example/tech-innovation/ai)

[Technology](https://business-news.example/tech-innovation)

[Money & Markets](https://business-news.example/money-markets)

[Leadership](https://business-news.example/leadership)

[Lifestyle](https://business-news.example/lifestyle)

[Video](https://business-news.example/video)

[Latest](https://business-news.example/latest)

#### Get Example Business News in your inbox

Free daily briefing on global business news.

Sign me up

Switch to Dark Mode

Menu

Business News

[Airlines](https://business-news.example/business-news/airlines)[Automobiles](https://business-news.example/business-news/autos)[Food](https://business-news.example/business-news/food)[Politics & Government](https://business-news.example/business-news/politics-government)[Retail & Ecommerce](https://business-news.example/business-news/retail)

Menu

Technology

[A.I.](https://business-news.example/tech-innovation/ai)[Computing](https://business-news.example/tech-innovation/cloud-computing)[Consumer Tech](https://business-news.example/tech-innovation/consumer-tech)

Menu

Money & Markets

[Economic Indicators](https://business-news.example/money-markets/economic-indicators)[Markets](https://business-news.example/money-markets/markets)[Personal Finance](https://business-news.example/money-markets/personal-finance)

Menu

Lifestyle

[Cars & Bikes](https://business-news.example/lifestyle/cars)[Collecting](https://business-news.example/lifestyle/collecting)[Entertainment](https://business-news.example/lifestyle/entertainment)[Food & Fine Dining](https://business-news.example/lifestyle/food-fine-dining)[Health and Fitness](https://business-news.example/lifestyle/health-fitness)[Real Estate](https://business-news.example/lifestyle/real-estate)[Travel](https://business-news.example/lifestyle/travel)

# 404

This page could not be found.

[Go home](https://business-news.example/)

![Image 6: Example Business News](https://images.example/logo.svg)

Global business news for a smarter world`

	// advisorSiteNotFoundBody is titled "Example Advisor": the notice
	// sits deep into the answer, behind its menus.
	advisorSiteNotFoundBody = `## [Helping advisors enable clients to achieve their financial goals](https://advisor.example/)Toggle navigation

[Advanced Search](https://advisor.example/search)

*   [Newsletter](https://advisor.example/newsletter/subscribe)[Log In](https://advisor.example/subscriber/login)

Toggle navigation
*   [Insights & Education](https://advisor.example/insights)
    *   [Articles](https://advisor.example/articles)
    *   [Charts](https://advisor.example/charts)

*   [Podcasts & Videos](https://advisor.example/podcasts)
    *   [Gaining Perspectives Podcast](https://advisor.example/podcasts/gaining-perspective)
    *   [ETF of the Week Podcast](https://advisor.example/podcasts/etf-of-the-week)
    *   [All Podcasts](https://advisor.example/podcasts)
    *   [Videos](https://advisor.example/videos)

[Subscribe to newsletter](https://advisor.example/newsletter/subscribe)[Log In](https://advisor.example/subscriber/login)

[Advanced Search](https://advisor.example/search)

Close ×

## 404 Error: Not Found

We're sorry but we seem to be experiencing some technical difficulties. Please contact [our support staff](mailto:support@advisor.example) for assistance.

#### Sponsored Content`

	// Parked, for-sale and expired domains.

	// registrarForSaleBody is a registrar's sale page, its domain's name
	// dropped.
	registrarForSaleBody = `###### The domain name

###### is for sale!

Premium

Verified Domain

##### Get a price in less than 24 hours

* * *

First Name

Last Name

Email

Phone

United States

By submitting, you agree to our terms.

Need a price instantly? Contact us now.

[1-855-646-1390](https://registrar.example/forsale/true)(Toll Free in the U.S. and Canada)

[+1 781-373-6808](https://registrar.example/forsale/true)(International number)

![Image 1: Safe & secure transactions](https://images.example/secure-transactions.svg)

Safe & secure transactions

![Image 2: Fast & easy transfers](https://images.example/fast-and-easy-transfers.svg)

Fast & easy transfers

![Image 3: Hassle free payments](https://images.example/hassle-free-payments.svg)

Hassle free payments

* * *

* * *

###### The simple, and safe way to buy domain names

No matter what kind of domain you want to buy or lease, we make the transfer simple and safe.`

	// registrarExpiredBody is an expired-domain page.
	registrarExpiredBody = `## Domain registration has expired.

### Renewal instructions

If you own **example.info**, you may still be able to renew it through your **Example Registrar** account. To renew: sign in, open **Domain List**, select **example.info**, and choose **Renew**.

[Sign in](https://registrar-b.example/login) · [Renewal help](https://registrar-b.example/help) · [Renewal and redemption fees](https://registrar-b.example/fees)

[Renew this domain](https://registrar-b.example/renew)`

	// registrarRegisteredBody is untitled.
	registrarRegisteredBody = `[![Image 1: Example Registrar Logo](https://images.example/logo.svg)](https://registrar-b.example/)

has been recently registered with example-registrar.com

## Want a domain name like this?

Discover domains on auction now

[See all auctions](https://registrar-b.example/market/)

## Download the Example Auctions app

Take auctions with you wherever you go

*   [![Image 2: Download on the App Store](https://images.example/appstore.svg)](https://apps.example/auctions)
*   [![Image 3: Get it on Google Play](https://images.example/googleplay.svg)](https://play.example/auctions)`

	// registrarParkedBody is untitled: a registrar's parked page.
	registrarParkedBody = `## example.com

## is a totally awesome idea still being worked on.

Check back later.

*   [Home](https://registrar-c.example/?source=parked)
*   [Transfer](https://registrar-c.example/transfer_in?source=parked)
*   [Renew](https://registrar-c.example/renew?source=parked)
*   [Domain Pricing](https://registrar-c.example/domain_pricing?source=parked)
*   [Email](https://registrar-c.example/email?source=parked)
*   [About Us](https://registrar-c.example/about?source=parked)
*   [Help](https://registrar-c.example/help?source=parked)
*   [Your Account](https://registrar-c.example/tools?source=parked)

*   [](https://social.example/hover)
*   [](https://photos.example/hover_domains)`

	// dnsParkedBody is titled "Parked Domain | ExampleDNS".
	dnsParkedBody = `[![Image 1: ExampleDNS](https://images.example/dns-logo.png)](https://dns.example/)

finance-site.example

is yet another domain managed by ExampleDNS

[This Domain is Parked: Learn more](https://finance-site.example/learn-more "This Domain is Parked: Learn more")

![Image 2](https://images.example/easybolt.svg)

Register, transfer or host your domains and websites with ExampleDNS

[Learn More](https://dns.example/)

### Podcast

[Video 3](https://video.example/watch?v=example)

[Subscribe](https://podcast.example/join)

### Quick and easy Reverse Tunnels

![Image 3](https://images.example/tunnel.jpg)

[Free localhost tunnels in under 60 seconds](https://tunnel.example/)

### You deploy, we handle the hosting

![Image 4](https://images.example/hosting.jpg)

[First Month Free (Promo code: WELCOME26)](https://hosting.example/)

### It's Always The DNS...

![Image 5](https://images.example/dns.jpg)

[But it doesn't have to be...](https://dns.example/dns)`

	// searchAdsParkingBody is untitled: a search-ads parking page.
	searchAdsParkingBody = `[](https://wallpaper.example/)

Related Search Topics

*    [Wallpaper For Rooms](https://ads.example/lander?serveURL=https%3A%2F%2Fwallpaper.example&system=PW&domain=wallpaper.example&feedid=2&type=PW_ROLLER_ADS_BACKFILL&qs=example-query-token)

Copyright © All rights reserved.[Privacy Policy](https://wallpaper.example/#!)`

	// newsPortalParkedBody is titled "Example News Portal": a parking page
	// with a sign-in box at its top.
	newsPortalParkedBody = `[![Image 1: Example Banner](https://images.example/news-logo.png)](https://game-site.example/)

*   [](javascript:void(0);)
*   [News](https://game-site.example/ "News")

*   [Entertainment](https://game-site.example/Entertainment "Entertainment")

*   [Weather](https://game-site.example/Weather "Weather")

*   [Sports](https://game-site.example/Sports "Sports")

*   [Finance](https://game-site.example/Finance "Finance")

*   [Lifestyle](https://game-site.example/Lifestyle "Lifestyle")

*   [Health](https://game-site.example/Health "Health")

*   [Dining](https://game-site.example/Dining "Dining")

*   [Travel](https://game-site.example/Travel "Travel")

*   [Autos](https://game-site.example/Autos "Autos")

*   [Video](https://game-site.example/Video "Video")

[Sign-up](javascript:void(0))

[![Image 2: User](https://images.example/user.svg)](https://game-site.example/#)

LOGIN

User Name *

Password *

[Sign-up](javascript:void(0))[Forgot Password?](https://game-site.example/#)

Login

User Name *

Submit

[← Back to Login](https://game-site.example/#)

This domain name may be for sale. [Click here to find out or call +1-866-284-4125](https://registrar.example/forsale/game-site.example)

*   [Home](https://game-site.example/?d=game-site.example&pcid=56)`

	// Bot checks on pages of 2 KiB or less.

	// searchEngineSorryBody is titled with the search URL: an unusual-traffic page.
	searchEngineSorryBody = `**About this page**

Our systems have detected unusual traffic from your computer network. This page checks to see if it's really you sending the requests, and not a robot. [Why did this happen?](https://search.example/search?q=example)

IP address: 2001:db8::1
Time: 2026-09-28T07:05:20Z
URL: https://search.example/search?q=example`

	// searchEngineSorryTitle is the search URL, as the page titles it.
	searchEngineSorryTitle = "https://search.example/search?q=example"

	// searchEngineSorryLongBody is a longer unusual-traffic page: it quotes the
	// search URL twice.
	searchEngineSorryLongBody = `**About this page**

Our systems have detected unusual traffic from your computer network. This page checks to see if it's really you sending the requests, and not a robot. [Why did this happen?](https://search.example/search?q=example&source=long)

IP address: 2001:db8::2
Time: 2026-09-28T07:17:13Z
URL: https://search.example/search?q=example&source=long`

	// cdnChallengeBody is titled "Client Challenge": a CDN's bot check.
	cdnChallengeBody = `A required part of this site couldn’t load. This may be due to a browser extension, network issues, or browser settings. Please check your connection, disable any ad blockers, or try using a different browser.

![Image 1: CDN Logo](https://images.example/fastly-logo.svg)

![Image 2](https://images.example/error-icon.svg)Oops, something went wrong.

Please check your connection, disable any ad blockers, or try using a different browser.`

	// Sign-in walls: thin pages whose link URLs put them over minArticleBytes.

	// socialSignInBody is titled "Example Social".
	socialSignInBody = `![Image 1](blob:https://social.example/1)![Image 2](blob:https://social.example/2)

See everyday moments from your close friends.

![Image 3](https://images.example/social.webp)

Log into Example Social

Mobile number, username or email

Password

- [x]

Log in



[Forgot password?](https://social.example/accounts/password/reset/)

Log in with Example Network

[Create new account](https://social.example/accounts/emailsignup/)

* * *

[About](https://social.example/about/)

[Blog](https://social.example/blog/)

[Jobs](https://social.example/careers)

[Help](https://social.example/help/)

[API](https://social.example/docs)

[Privacy](https://social.example/legal/privacy/)

[Terms](https://social.example/legal/terms/)

[Locations](https://social.example/explore/locations/)

[Popular](https://social.example/popular/)

[Lite](https://social.example/web/lite/)

English

© 2026 Example Social`

	// networkSignInBody is titled "Example Network".
	networkSignInBody = `Explore the things you love.

Log into Example Network

Email or mobile number

Password

- [x]

Log in



[Forgot password?](https://network.example/recover/initiate/)

[Create new account](https://network.example/reg/)

* * *

* * *

English (US)

[Español](https://network.example/notes/#)

[Français (France)](https://network.example/notes/#)

[中文(简体)](https://network.example/notes/#)

Example Network helps you connect with the people you know and discover communities around the things you care about. Share updates, photos and links, join groups, and keep up with the people and pages you follow. The service is available in many languages and on many devices, with tools for messaging, live video and events.`

	// professionalSignInBody is titled "Example Network Login, Sign in | Example
	// Network".
	professionalSignInBody = `## 0 notifications

[](https://professional-network.example/)

[](https://professional-network.example/)

# Sign in

New to Example Network?

[Join now](https://professional-network.example/signup/cold-join/?fromLogin=true)

Continue with Example Continue with Example

Sign in with Example

By continuing, you agree to Example Network’s[**User Agreement**](https://professional-network.example/legal/user-agreement/),[**Privacy Policy**](https://professional-network.example/legal/privacy-policy/), and[**Cookie Policy**](https://professional-network.example/legal/cookie-policy/).

* * *

or

* * *

Email or phone



Password



[Forgot password?](https://professional-network.example/passwordReset/)

- [x]

Keep me signed in

Sign in`

	// An article whose opening holds a count on a line of its own, as a
	// status code alone would be.

	// stackOverflowQuestionBody has its score, 444, on a line of its own in
	// its opening, as some Q&A questions give theirs.
	stackOverflowQuestionBody = `This question shows research effort; it is useful and clear

444

This question does not show any research effort; it is unclear or not useful

Save this question.

[](https://qa.example/posts/17074365/timeline)

Show activity on this post.

I recently updated a framework and noticed the view's bounds is not always resized to account for the status bar and navigation bar.

In the layout callback, I print the view's bounds:

> {{0, 0}, {320, 568}}

This results in my content appearing below the navigation bar and status bar.

I know I could account for the height myself by getting the main screen's height, subtracting the status bar's height and navigation bar's height, but that seems like unnecessary extra work.

How can I fix this issue?

**Update:**

I've found a solution for this specific problem. Set the navigation bar's translucent property to NO.

    self.navigationController.navigationBar.translucent = NO;

This will fix the view from being framed underneath the navigation bar and status bar.`

	// stackOverflowQuestionTitle is the question's title.
	stackOverflowQuestionTitle = "Status bar and navigation bar appear over my view's bounds in iOS 7"
)
